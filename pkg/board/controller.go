package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
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
func (c *Controller) ReconcileAll() error {
	cards, err := c.store.ListCards()
	if err != nil {
		return fmt.Errorf("list cards: %w", err)
	}
	for _, card := range cards {
		c.Start(card)
	}
	return nil
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
	// lastSnapErr is the last snapshot failure logged. The loop retries every
	// 500ms, so a persistent failure is logged once, when it appears or
	// changes, not on every retry.
	lastSnapErr := ""
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
			if msg := err.Error(); msg != lastSnapErr {
				lastSnapErr = msg
				log.Printf("card %s: snapshot failed: %v", cardID, err)
			}
			// The control plane is unreachable. If the agent's tmux pane is
			// gone, relaunch to resume; otherwise it is still starting, so just
			// wait and retry.
			if alive, aerr := c.sessions.Alive(card.Session); aerr != nil {
				log.Printf("card %s: liveness check of session %s failed: %v", cardID, card.Session, aerr)
			} else if !alive {
				_ = c.relaunch(card, "")
			}
			if sleep(ctx, retryDelay) {
				return
			}
			continue
		}
		if lastSnapErr != "" {
			lastSnapErr = ""
			log.Printf("card %s: control plane answered", cardID)
		}

		c.setTitleFromSnapshot(card, snap)

		// The control plane answers: the agent has started. If the card is
		// still marked starting, default to waiting; the event replay below
		// promptly corrects it if a turn is already underway. Checking the
		// loop-top read is safe: this watcher is the only status writer.
		if card.Status == StatusStarting {
			c.setStatus(cardID, StatusWaiting)
		}

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
		//
		// Delivery of stream events is best-effort: the agent emits
		// stream_stopped non-blockingly during teardown, and its TUI drops
		// events for slow control-plane subscribers before they reach the
		// replay buffer. A dropped start or stop therefore skews the depth for
		// good — reconnecting and replaying cannot recover it. user_message is
		// the recovery point: the runtime emits it only for real user turns
		// (never for sub-agent or skill sub-sessions), right before the turn's
		// outermost stream_started, so it resets the depth the same way the
		// agent's own TUI zeroes its stream depth on every submit.
		depth := 0
		// failed marks that the current turn emitted an error event. The card is
		// turned red the moment that event arrives — it is delivered reliably,
		// while the stream_stopped that follows is best-effort and may be dropped,
		// so waiting for the stop would leave a failed turn stuck as running. But
		// an error is not always turn-fatal: a sub-agent's failure surfaces as an
		// error event that the parent recovers from, so the outermost stop's
		// reason is authoritative — "normal" means the turn completed and clears
		// the flag. Otherwise it stays set until the next turn begins.
		failed := false
		// paused marks that the run loop is blocked on /pause (runtime_paused).
		// There is no matching resume event, so any subsequent event — the loop
		// emits nothing while blocked — means the session resumed.
		paused := false

		// Events at or below the snapshot's seq are replayed history. Their
		// intermediate statuses must not be broadcast on every reconnect — a
		// long-resolved error would flash the card red each time — so they only
		// update the derived state, which is applied once, when the replay
		// catches up with the snapshot. Replayed titles are dropped entirely:
		// the snapshot's title already reflects them. Events without a seq are
		// treated as live.
		replaying := snap.LastEventSeq > 0
		var replayStatus CardStatus
		flushReplay := func() {
			replaying = false
			if replayStatus != "" {
				c.setStatus(cardID, replayStatus)
			}
		}
		setStatus := func(status CardStatus) {
			if replaying {
				replayStatus = status
			} else {
				c.setStatus(cardID, status)
			}
		}

		exited := false
		_ = client.StreamEvents(ctx, 0, func(ev agent.Event) bool {
			if replaying && (ev.Seq == 0 || ev.Seq > snap.LastEventSeq) {
				flushReplay() // past the snapshot: this event is live
			}
			switch ev.Type {
			case agent.EventGap:
				return false // resume point evicted: reconnect and re-snapshot
			case agent.EventSessionExited:
				exited = true
				return false
			case agent.EventUserMessage:
				// A new user turn begins: any leftover depth is drift from
				// dropped stream events. Resync here so one lost stop cannot
				// leave the card stuck running forever.
				depth = 0
				failed = false
			case agent.EventStreamStarted:
				failed = false
				paused = false
				depth++
				setStatus(StatusRunning)
			case agent.EventError:
				failed = true
				paused = false
				setStatus(StatusError)
			case agent.EventStreamStopped:
				paused = false
				if depth > 0 {
					depth--
				}
				if depth == 0 {
					// The outermost stream ended: a "normal" reason means the
					// turn completed even if a nested sub-agent errored along
					// the way, so the sticky error is cleared. Any other reason
					// (error, hook_blocked, or a dropped/empty one) leaves a
					// failed turn red.
					if ev.Reason == agent.ReasonNormal {
						failed = false
					}
					if !failed {
						setStatus(StatusWaiting)
					}
				}
			case agent.EventRuntimePaused:
				paused = true
				setStatus(StatusPaused)
			case agent.EventSessionTitle:
				if !replaying {
					c.setTitle(cardID, ev.Title)
				}
			default:
				// The run loop emits nothing while blocked on /pause, so any
				// other event means the session resumed mid-turn.
				if paused {
					paused = false
					setStatus(StatusRunning)
				}
			}
			if replaying && ev.Seq == snap.LastEventSeq {
				flushReplay() // caught up with the snapshot
			}
			return true
		})

		if exited && ctx.Err() == nil {
			// The agent process ended; resume it so the card stays usable.
			log.Printf("card %s: agent exited", cardID)
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
// ordering) and ensures it is watched. When requireIdle is set, a running
// card is rejected atomically with the move. The status is left untouched:
// the color tracks the agent's activity, not the move. Prompt delivery is a
// separate step ([Controller.SendPrompt]): the move must be observable even
// when the prompt cannot be delivered.
func (c *Controller) MoveCardToColumn(card *Card, column string, requireIdle bool) error {
	moved, err := c.store.MoveCard(card.ID, column, requireIdle)
	if err != nil {
		return fmt.Errorf("move card: %w", err)
	}
	*card = *moved

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
	log.Printf("card %s: relaunching agent (tmux session %s, agent session %s)", card.ID, card.Session, card.AgentSession)
	_ = c.sessions.KillSession(card.Session)
	socket := socketPath(card.AgentSession)
	// A killed agent (e.g. after a Docker Desktop restart) leaves its control-
	// plane socket file behind. Remove it so the resumed run can bind --listen;
	// otherwise the new agent fails to start and the card stays stuck "starting".
	_ = os.Remove(socket)
	err := c.sessions.NewSession(
		card.Session, card.Worktree, card.Agent, card.AgentSession,
		socket, "", "", prompt,
	)
	if err != nil {
		log.Printf("card %s: relaunch failed: %v", card.ID, err)
		return err
	}
	// The agent is launching again: show it as starting until its control
	// plane answers and the event stream drives the status.
	c.setStatus(card.ID, StatusStarting)
	return nil
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
