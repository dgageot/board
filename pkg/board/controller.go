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
	"github.com/dgageot/board/pkg/git"
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
	// prLookupTimeout bounds the `gh pr list` search for a card's PR: it
	// reaches out to GitHub, so a stalled or unauthenticated CLI must not hang
	// the lookup forever.
	prLookupTimeout = 10 * time.Second
	// expectTurnTimeout bounds how long an expected first turn may hold a
	// card at "starting". If the launch prompt never runs (dropped, or
	// canceled by a user attached to the TUI), the card falls back to waiting
	// instead of sitting blue — and unmovable, starting counts as busy —
	// forever. Generous: agent init (MCP servers, RAG indexing…) can be slow.
	expectTurnTimeout = 5 * time.Minute
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
	// prURLForHead finds the PR whose head commit is the worktree's HEAD. It
	// is a field so tests can inject a fake without a real `gh` and GitHub repo.
	prURLForHead func(ctx context.Context, worktree string) (string, error)

	mu       sync.Mutex
	watchers map[string]*watcher
	// expectTurn marks cards whose latest launch carried an initial prompt: a
	// first turn is imminent, so the watcher keeps them "starting" until the
	// event stream reports it instead of flashing green ("waiting") first.
	// Without it, a card whose agent spends a while initializing (MCP servers,
	// RAG indexing…) before running the prompt looks done when it never ran.
	// The value is the launch time: an expectation older than
	// expectTurnTimeout no longer holds the card. In-memory only: a board
	// restart forgets it, reopening the early green flash for cards mid-launch
	// — a narrow window that self-corrects at stream_started.
	expectTurn map[string]time.Time
}

// watcher tracks a running watch goroutine so it can be cancelled and waited on.
type watcher struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func newController(ctx context.Context, store Store, sessions SessionManager, onChanged func()) *Controller {
	return &Controller{
		ctx:          ctx,
		store:        store,
		sessions:     sessions,
		onChanged:    onChanged,
		clientFor:    func(socket, session string) sessionClient { return agent.NewClient(socket, session) },
		prURLForHead: git.PRURLForHead,
		watchers:     make(map[string]*watcher),
		expectTurn:   make(map[string]time.Time),
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

// ExpectTurn records that the card's agent was just launched with an initial
// prompt, so its first turn is imminent.
func (c *Controller) ExpectTurn(cardID string) {
	c.setExpectTurn(cardID, true)
}

func (c *Controller) setExpectTurn(cardID string, expect bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if expect {
		c.expectTurn[cardID] = time.Now()
	} else {
		delete(c.expectTurn, cardID)
	}
}

// turnExpected reports whether the card should be held at "starting" because
// its launch prompt's first turn has not been seen yet, and for how much
// longer the hold may last. An expectation past expectTurnTimeout is dropped:
// the prompt was likely lost, and waiting is a recoverable misreport while a
// stuck "starting" card cannot even be moved.
func (c *Controller) turnExpected(cardID string) (bool, time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	since, ok := c.expectTurn[cardID]
	if !ok {
		return false, 0
	}
	remaining := expectTurnTimeout - time.Since(since)
	if remaining <= 0 {
		delete(c.expectTurn, cardID)
		log.Printf("card %s: expected turn never started; showing as waiting", cardID)
		return false, 0
	}
	return true, remaining
}

// Stop cancels the card's watcher and waits for it to exit. Waiting matters:
// it guarantees the watcher cannot relaunch the session after the caller goes
// on to tear it down (kill the tmux session, remove the worktree), which would
// otherwise leave an orphaned session.
func (c *Controller) Stop(cardID string) {
	c.mu.Lock()
	w, ok := c.watchers[cardID]
	delete(c.watchers, cardID)
	delete(c.expectTurn, cardID)
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
	// relaunchedUnsupported guards the once-per-watcher relaunch of a session
	// whose control plane lacks GET /snapshot: an agent left running by an old
	// docker-agent binary. Relaunching resumes the session on the binary
	// currently installed; if that one is still too old, relaunching again
	// would only kill and restart the agent in a loop.
	relaunchedUnsupported := false
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
			// The control plane is unreachable or unusable. If the agent's
			// tmux pane is gone — or it is alive but predates GET /snapshot
			// (old docker-agent binary) — relaunch to resume on the current
			// binary; otherwise the agent is still starting, so wait and retry.
			unsupported := errors.Is(err, agent.ErrUnsupported) && !relaunchedUnsupported
			if alive, aerr := c.sessions.Alive(card.Session); aerr != nil {
				log.Printf("card %s: liveness check of session %s failed: %v", cardID, card.Session, aerr)
			} else if !alive || unsupported {
				relaunchedUnsupported = relaunchedUnsupported || unsupported
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
		c.setCost(cardID, snap.Cost)
		// Look up the card's PR by worktree HEAD off the watcher loop: it shells
		// out to `gh` (a network call), which must not block event processing.
		go c.refreshPRURL(ctx, cardID)

		// The control plane answers: the agent has started. If the card is
		// still marked starting, default to waiting; the event replay below
		// promptly corrects it if a turn is already underway. Checking the
		// loop-top read is safe: the only other status writer is relaunch,
		// which writes "starting" — after setting the turn expectation when it
		// carries a prompt — so a stale read at worst delays this transition
		// to the next loop iteration.
		// Exception: a launch that carried an initial prompt is about to run
		// its first turn — the control plane answers while the agent is still
		// initializing (MCP servers, RAG indexing…), before the TUI submits
		// the prompt — so the card stays "starting" until stream_started flips
		// it to running. Flashing green ("waiting") before the first turn
		// would misreport an agent that is still working as done. The hold is
		// bounded: while it lasts, the event stream below is given a deadline
		// so a turn that never starts cuts a quiet stream, re-runs this check
		// and falls back to waiting — the stream would otherwise block forever
		// and never re-evaluate the expectation. When the turn does arrive the
		// deadline just forces one spurious reconnect, which replays cheaply.
		streamCtx, streamCancel := ctx, context.CancelFunc(func() {})
		if card.Status == StatusStarting {
			if expected, remaining := c.turnExpected(cardID); !expected {
				c.setStatus(cardID, StatusWaiting)
			} else {
				streamCtx, streamCancel = context.WithTimeout(ctx, remaining)
			}
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
		_ = client.StreamEvents(streamCtx, 0, func(ev agent.Event) bool {
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
				c.setExpectTurn(cardID, false) // the expected turn arrived
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
					// A turn just finished: the event stream carries neither the
					// session's cost nor any PR the agent opened, so re-snapshot
					// and mirror both onto the card. Only for live turns —
					// replayed history is already reflected in the snapshot read
					// at the loop top.
					if !replaying {
						go c.refreshFromSnapshot(ctx, cardID, client)
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
		streamCancel()

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

// setCost writes only the cost field, and only on change, broadcasting so
// clients refresh. A zero cost from a snapshot with no billed messages yet is
// ignored so it never clears a cost the card already shows.
func (c *Controller) setCost(cardID string, cost float64) {
	if cost == 0 {
		return
	}
	card, err := c.store.GetCard(cardID)
	if err != nil || card.Cost == cost {
		return
	}
	if c.store.UpdateCardCost(cardID, cost) == nil {
		c.onChanged()
	}
}

// refreshFromSnapshot re-reads the session snapshot and mirrors the fields the
// event stream does not carry — the cumulative cost, and any pull request the
// agent opened — onto the card. It is called when a turn finishes. Failures
// are ignored: the next loop-top snapshot will reconcile, and a transient
// control-plane hiccup must not stop the watcher. It runs in its own goroutine
// off the watcher's event loop; the passed context (the watcher's) bounds and
// cancels the work.
func (c *Controller) refreshFromSnapshot(ctx context.Context, cardID string, client sessionClient) {
	sctx, scancel := context.WithTimeout(ctx, snapshotTimeout)
	defer scancel()
	if snap, err := client.Snapshot(sctx); err == nil {
		c.setCost(cardID, snap.Cost)
	}
	c.refreshPRURL(ctx, cardID)
}

// refreshPRURL finds the pull request whose head commit is the card's worktree
// HEAD and records its URL. The PR is identified by commit SHA, not branch
// name, so a branch the agent pushed under a different name (e.g. to a fork)
// still matches. It writes only on change and never clears a URL the card
// already shows: an unpushed local commit or a transient `gh` failure simply
// leaves the last known PR in place. The context bounds the `gh` lookup.
func (c *Controller) refreshPRURL(ctx context.Context, cardID string) {
	card, err := c.store.GetCard(cardID)
	if err != nil {
		return
	}
	lookupCtx, cancel := context.WithTimeout(ctx, prLookupTimeout)
	defer cancel()
	prURL, err := c.prURLForHead(lookupCtx, card.Worktree)
	if err != nil || prURL == "" || prURL == card.PRURL {
		return
	}
	if c.store.UpdateCardPRURL(cardID, prURL) == nil {
		c.onChanged()
	}
}

// Ready reports whether the card's agent control plane answers, i.e. the agent
// process has really started and its UI is worth showing. The Agent button
// consults it only while the card is still starting, to avoid attaching a
// terminal to a session showing the bare docker-agent launch command.
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
	// plane answers and the event stream drives the status. A prompt-bearing
	// relaunch runs that prompt as its first turn, so the card must stay
	// "starting" until the turn's stream_started — not flash green while the
	// agent is still initializing.
	c.setExpectTurn(card.ID, prompt != "")
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
