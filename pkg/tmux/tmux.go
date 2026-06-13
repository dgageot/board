package tmux

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"

	"al.essio.dev/pkg/shellescape"
	"github.com/GianlucaP106/gotmux/gotmux"
)

// Sessions provides session management operations backed by tmux.
type Sessions struct{}

// defaultTmux returns a process-wide [gotmux.Tmux] handle. The handle is
// stateless aside from the socket path it remembers, so it can be safely
// reused across goroutines and we avoid spawning a fresh helper for every
// call.
var defaultTmux = sync.OnceValues(gotmux.DefaultTmux)

// sessionDefaults are tmux options applied to every session so the embedded
// session feels like a native terminal: no tmux chrome, keys passed straight
// through, full terminal fidelity, and client-driven sizing.
var sessionDefaults = [][]string{
	// Visual chrome: hide every bit of tmux UI.
	{"set", "-g", "status", "off"},
	{"set", "-g", "visual-bell", "off"},
	{"set", "-g", "visual-activity", "off"},
	{"set", "-g", "visual-silence", "off"},
	{"set", "-g", "bell-action", "none"},
	{"set", "-g", "monitor-activity", "off"},
	{"set", "-g", "monitor-bell", "off"},
	{"set", "-g", "display-time", "1"},
	{"set", "-g", "pane-border-status", "off"},
	{"set", "-g", "pane-border-lines", "simple"},

	// Input behavior: every keystroke reaches the agent, ESC is instant.
	{"set", "-g", "prefix", "none"},
	{"set", "-g", "prefix2", "none"},
	{"unbind", "C-b"},
	{"set", "-g", "escape-time", "0"},
	{"set", "-g", "mouse", "on"},

	// Terminal fidelity: truecolor, clipboard, focus events.
	{"set", "-g", "allow-passthrough", "on"},
	{"set", "-g", "focus-events", "on"},
	{"set", "-g", "set-clipboard", "on"},
	{"set", "-g", "default-terminal", "tmux-256color"},
	{"set", "-ga", "terminal-features", ",xterm-256color:clipboard:ccolour:cstyle:focus:title:mouse:RGB"},
	{"set-environment", "LANG", "en_US.UTF-8"},
	{"set-environment", "LC_ALL", "en_US.UTF-8"},

	// Sizing: follow the attached client, not the smallest one.
	{"set", "-g", "aggressive-resize", "on"},
	{"set", "-g", "window-size", "latest"},
}

// applySessionDefaults applies [sessionDefaults] to the given session.
// The -t flag must follow the subcommand; tmux rejects it as a global flag.
func applySessionDefaults(sessionName string) {
	for _, args := range sessionDefaults {
		_ = tmuxRun(append([]string{args[0], "-t", sessionName}, args[1:]...)...)
	}
}

// agentCommand builds the docker agent invocation for a session. The board
// owns sessionID and passes it via --session: the first run creates that
// session, later runs resume it.
//
// --listen exposes the run's control plane on listenSocket (a unix socket the
// board owns), so the board can observe and drive the session over HTTP
// instead of scraping the terminal.
//
// On the first run, worktreeName is non-empty: --worktree creates an isolated
// git worktree (branched from origin/main) and every tool runs inside it. On
// resume, worktreeName is empty and --worktree is omitted: docker agent
// reattaches the session to its original worktree automatically, so passing
// --worktree again (which would fail, the worktree already exists) is avoided.
//
// A non-empty prompt is appended as the first message.
func agentCommand(agent, sessionID, listenSocket, worktreeName, prompt string) string {
	cmd := fmt.Sprintf("docker agent run %s --yolo --session %s --listen %s",
		agent, shellescape.Quote(sessionID), shellescape.Quote("unix://"+listenSocket))
	if worktreeName != "" {
		cmd += fmt.Sprintf(" --worktree=%s --worktree-base origin/main", shellescape.Quote(worktreeName))
	}
	if prompt != "" {
		cmd += " " + shellescape.Quote(prompt)
	}
	return cmd
}

// NewSession creates a tmux session and runs docker agent in it. The agent is
// exec'd into the pane (replacing the shell) so that, when it exits, the pane
// becomes a dead pane instead of dropping back to a shell. Combined with
// remain-on-exit, the tmux session outlives a dead agent: the user can still
// read its final output and the controller can detect the dead pane and relaunch.
//
// A non-empty worktreeName marks the first run: workDir is then the repository
// and --worktree branches a new worktree from it. On resume worktreeName is
// empty and workDir is the existing worktree directory, so the agent stays
// isolated there.
func (Sessions) NewSession(sessionName, workDir, agent, sessionID, listenSocket, worktreeName, prompt string) error {
	tmux, err := defaultTmux()
	if err != nil {
		return fmt.Errorf("tmux init: %w", err)
	}

	session, err := tmux.NewSession(&gotmux.SessionOptions{
		Name:           sessionName,
		StartDirectory: workDir,
	})
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	applySessionDefaults(sessionName)

	// Keep the pane (and thus the session) alive after the agent exits. Set
	// while the shell is still running so there is no race where the agent
	// could exit before the option takes effect. Scoped to this session so we
	// do not change the behaviour of the user's other tmux panes.
	_ = tmuxRun("set-option", "-t", sessionName, "remain-on-exit", "on")

	panes, err := session.ListPanes()
	if err != nil {
		return fmt.Errorf("list panes: %w", err)
	}
	if len(panes) == 0 {
		return errors.New("no panes in session")
	}

	// exec replaces the shell with the agent so the agent becomes the pane's
	// process: when it exits the pane goes dead (see remain-on-exit above).
	if err := panes[0].SendKeys("exec " + agentCommand(agent, sessionID, listenSocket, worktreeName, prompt)); err != nil {
		return fmt.Errorf("send keys: %w", err)
	}
	if err := panes[0].SendKeys("Enter"); err != nil {
		return fmt.Errorf("send enter: %w", err)
	}

	return nil
}

// tmuxRun runs `tmux <args...>` and returns combined output as part of any error.
func tmuxRun(args ...string) error {
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", out, err)
	}
	return nil
}

// KillSession kills a tmux session.
func (Sessions) KillSession(sessionName string) error {
	tmux, err := defaultTmux()
	if err != nil {
		return err
	}

	session, err := tmux.GetSessionByName(sessionName)
	if err != nil || session == nil {
		return nil // session doesn't exist, nothing to kill
	}

	return session.Kill()
}

// Alive reports whether the session exists and its agent pane is still
// running. A pane goes dead when the agent exits (remain-on-exit keeps the
// session around); a missing session reports not alive. The board uses this
// to tell a slow-starting control plane from a session that needs relaunching.
func (Sessions) Alive(sessionName string) (bool, error) {
	tmux, err := defaultTmux()
	if err != nil {
		return false, err
	}

	session, err := tmux.GetSessionByName(sessionName)
	if err != nil {
		return false, err
	}
	if session == nil {
		return false, nil
	}

	panes, err := session.ListPanes()
	if err != nil {
		return false, err
	}
	if len(panes) == 0 {
		return false, nil
	}

	return !panes[0].Dead, nil
}
