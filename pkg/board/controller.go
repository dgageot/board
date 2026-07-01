package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/dgageot/board/pkg/agent"
)

// sessionClient is the slice of the control-plane client the controller needs.
// It is an interface so tests can inject a fake without real sockets.
type sessionClient interface {
	Snapshot(ctx context.Context) (agent.Snapshot, error)
	StreamEvents(ctx context.Context, since uint64, onEvent func(agent.Event) bool) error
	Followup(ctx context.Context, idempotencyKey, message string) (bool, error)
}

const (
	// retryDelay paces reconnect and relaunch attempts.
	retryDelay = 500 * time.Millisecond
	// snapshotTimeout bounds a single snapshot request so a wedged server
	// cannot block a watcher forever.
	snapshotTimeout = 10 * time.Second
	// followupTimeout bounds a single follow-up delivery.
	followupTimeout = 10 * time.Second
	// readyProbeTimeout bounds the control-plane probe behind the Agent button
	// so a click gets quick feedback instead of hanging.
	readyProbeTimeout = 2 * time.Second
)

// Controller keeps each card in sync with its agent's control plane. One
// watcher goroutine per card tails the session event stream and mirrors the
// running/waiting status and the title into the store, reconnecting — and
// relaunching the tmux session if the agent died — as needed. It replaces the
// old terminal-scraping poller: status, title and prompt delivery all go
// through the control plane.
type Controller struct {
	// ctx is the board-lifetime context watchers derive from; they are started
	// lazily (Start) after construction, so it is held here rather than passed.
	ctx       context.Context //nolint:containedctx // base context for background watchers
	store     Store
	sessions  SessionManager
	onChanged func()
	clientFor func(socket, session string) sessionClient

	mu       sync.Mutex
	watchers map[string]*watcher
}

// watcher tracks a running watch goroutine so it can be cancelled and waited on.
type watcher struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func newController(ctx context.Context, store Store, sessions SessionManager, onChanged func()) *Controller {
	return &Controller{
		ctx:       ctx,
		store:     store,
		sessions:  sessions,
		onChanged: onChanged,
		clientFor: func(socket, session string) sessionClient { return agent.NewClient(socket, session) },
		watchers:  make(map[string]*watcher),
	}
}

// ReconcileAll starts a watcher for every existing card. Called on startup so
// the board reattaches to sessions still running in tmux.
func (c *Controller) ReconcileAll() {
	cards, err := c.store.ListCards()
	if err != nil {
		return
	}
	for _, card := range cards {
		c.Start(card)
	}
}

// Start ensures a watcher is running for the card. Idempotent.
func (c *Controller) Start(card *Card) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.watchers[card.ID]; ok {
		return
	}
	ctx, cancel := context.WithCancel(c.ctx)
	w := &watcher{cancel: cancel, done: make(chan struct{})}
	c.watchers[card.ID] = w
	go func() {
		defer close(w.done)
		c.watch(ctx, card.ID)
	}()
}

// Stop cancels the card's watcher and waits for it to exit. Waiting matters:
// it guarantees the watcher cannot relaunch the session after the caller goes
// on to tear it down (kill the tmux session, remove the worktree), which would
// otherwise leave an orphaned session.
func (c *Controller) Stop(cardID string) {
	c.mu.Lock()
	w, ok := c.watchers[cardID]
	delete(c.watchers, cardID)
	c.mu.Unlock()
	if ok {
		w.cancel()
		<-w.done
	}
}

// watch keeps one card mirrored to its control plane: snapshot to resync, then
// tail events; on a drop, reconnect; if the agent is gone, relaunch and resume.
func (c *Controller) watch(ctx context.Context, cardID string) {
	for ctx.Err() == nil {
		card, err := c.store.GetCard(cardID)
		if errors.Is(err, sql.ErrNoRows) {
			return // card deleted
		}
		if err != nil {
			// Transient store error: back off and retry rather than abandoning
			// the card until the next board restart.
			if sleep(ctx, retryDelay) {
				return
			}
			continue
		}
		client := c.clientFor(socketPath(card.AgentSession), card.AgentSession)

		sctx, scancel := context.WithTimeout(ctx, snapshotTimeout)
		snap, err := client.Snapshot(sctx)
		scancel()
		if err != nil {
			// The control plane is unreachable. If the agent's tmux pane is
			// gone, relaunch to resume; otherwise it is still starting, so just
			// wait and retry.
			if alive, aerr := c.sessions.Alive(card.Session); aerr == nil && !alive {
				_ = c.relaunch(card, "")
			}
			if sleep(ctx, retryDelay) {
				return
			}
			continue
		}

		c.setTitleFromSnapshot(card, snap)

		// Derive the running state from the event stream, not snap.Streaming:
		// for attached (--listen) sessions that flag is always false because
		// turns run in the TUI, never through the server's RunSession (the only
		// place its streaming lock is held). Tail from the start of the buffer
		// (since 0) so the whole backlog is replayed: a turn that began before
		// this watcher connected — its stream_started already past the
		// snapshot's last seq — is still seen and keeps the card running.
		//
		// A turn can spawn nested streams: every sub-agent (transferred task)
		// and skill emits its own stream_started/stream_stopped pair. The depth
		// keeps the card running until the outermost stream stops, instead of
		// flipping to waiting the moment an inner sub-agent finishes while the
		// parent is still working. Replayed orphan stops, whose start was
		// evicted from the buffer, are clamped at zero.
		depth := 0
		// failed marks that the current turn emitted an error event. The card is
		// turned red the moment that event arrives — it is delivered reliably,
		// while the stream_stopped that follows is best-effort and may be dropped,
		// so waiting for the stop would leave a failed turn stuck as running. The
		// flag also keeps a delivered stop from reverting red to waiting. It stays
		// set across turns until the next one starts (stream_started).
		failed := false

		exited := false
		_ = client.StreamEvents(ctx, 0, func(ev agent.Event) bool {
			switch ev.Type {
			case agent.EventGap:
				return false // resume point evicted: reconnect and re-snapshot
			case agent.EventSessionExited:
				exited = true
				return false
			case agent.EventStreamStarted:
				failed = false
				depth++
				c.setStatus(cardID, StatusRunning)
			case agent.EventError:
				failed = true
				c.setStatus(cardID, StatusError)
			case agent.EventStreamStopped:
				if depth > 0 {
					depth--
				}
				if depth == 0 && !failed {
					c.setStatus(cardID, StatusWaiting)
				}
			case agent.EventSessionTitle:
				c.setTitle(cardID, ev.Title)
			}
			return true
		})

		if exited && ctx.Err() == nil {
			// The agent process ended; resume it so the card stays usable.
			_ = c.relaunch(card, "")
		}
		if sleep(ctx, retryDelay) {
			return
		}
	}
}

// setTitleFromSnapshot mirrors a fresh snapshot's title into the card. The
// snapshot's streaming flag is deliberately ignored: it is unreliable for
// attached sessions (see watch), so running/waiting is driven entirely by the
// stream_started/stream_stopped events.
func (c *Controller) setTitleFromSnapshot(card *Card, snap agent.Snapshot) {
	if snap.Title != "" {
		c.setTitle(card.ID, snap.Title)
	}
}

// setStatus writes only the status field, and only on change, broadcasting so
// clients refresh.
func (c *Controller) setStatus(cardID string, status CardStatus) {
	card, err := c.store.GetCard(cardID)
	if err != nil || card.Status == status {
		return
	}
	if c.store.UpdateCardStatus(cardID, status) == nil {
		c.onChanged()
	}
}

// setTitle writes only the title field, and only on change.
func (c *Controller) setTitle(cardID, title string) {
	card, err := c.store.GetCard(cardID)
	if err != nil || card.Title == title {
		return
	}
	if c.store.UpdateCardTitle(cardID, title) == nil {
		c.onChanged()
	}
}

// Ready reports whether the card's agent control plane answers, i.e. the agent
// process has really started and its UI is worth showing. It lets the Agent
// button avoid attaching a terminal to a session still showing the bare
// docker-agent launch command.
func (c *Controller) Ready(card *Card) bool {
	client := c.clientFor(socketPath(card.AgentSession), card.AgentSession)
	ctx, cancel := context.WithTimeout(c.ctx, readyProbeTimeout)
	defer cancel()
	_, err := client.Snapshot(ctx)
	return err == nil
}

// MoveCardToColumn moves a card to the given column, reinserts it (for
// ordering) and ensures it is watched. The status is left untouched: the
// color tracks the agent's activity, not the move. Prompt delivery is a
// separate step ([Controller.SendPrompt]): the move must be observable even
// when the prompt cannot be delivered.
func (c *Controller) MoveCardToColumn(card *Card, column string) error {
	card.Column = column

	if err := c.store.ReinsertCard(card); err != nil {
		return fmt.Errorf("reinsert card: %w", err)
	}

	c.Start(card) // no-op if already watching
	return nil
}

// SendPrompt delivers a prompt to the card's agent through the control plane.
// The follow-up carries an idempotency key so the control plane can dedupe a
// retried delivery. If the follow-up fails only because the agent (or its tmux
// session) is gone, the session is relaunched with the prompt as its next
// message; any other failure (busy, queue full, timeout) is surfaced rather
// than destroying a live session.
func (c *Controller) SendPrompt(card *Card, prompt string) error {
	if prompt == "" {
		return nil
	}

	client := c.clientFor(socketPath(card.AgentSession), card.AgentSession)
	ctx, cancel := context.WithTimeout(c.ctx, followupTimeout)
	defer cancel()
	if _, err := client.Followup(ctx, newID(), prompt); err == nil {
		return nil
	} else if alive, aerr := c.sessions.Alive(card.Session); aerr != nil || alive {
		return fmt.Errorf("deliver prompt: %w", err)
	}

	return c.relaunch(card, prompt)
}

// relaunch recreates the card's tmux session under the same name, resuming the
// same docker-agent session (and its worktree) on the same control-plane
// socket. A non-empty prompt is delivered as the resumed session's next
// message. Launching from the worktree keeps the agent isolated even if
// docker-agent's own worktree reattachment does not happen.
func (c *Controller) relaunch(card *Card, prompt string) error {
	_ = c.sessions.KillSession(card.Session)
	socket := socketPath(card.AgentSession)
	// A killed agent (e.g. after a Docker Desktop restart) leaves its control-
	// plane socket file behind. Remove it so the resumed run can bind --listen;
	// otherwise the new agent fails to start and the card stays stuck "starting".
	_ = os.Remove(socket)
	return c.sessions.NewSession(
		card.Session, card.Worktree, card.Agent, card.AgentSession,
		socket, "", "", prompt,
	)
}

// sleep waits for d or until ctx is done, reporting whether ctx was done.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}
