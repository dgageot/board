package tmux

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"

	"al.essio.dev/pkg/shellescape"
	"github.com/GianlucaP106/gotmux/gotmux"
)

// Sessions provides session management operations backed by tmux.
type Sessions struct{}

// SocketPath is the dedicated tmux socket the board runs its sessions on.
// Board shares the host's tmux binary but not its default server: a private
// socket keeps board's server-wide options (default-terminal,
// terminal-features, set-clipboard, escape-time, …) from leaking into the
// user's interactive tmux. The path is stable across board restarts so the
// controller can reattach to sessions left running on it.
func SocketPath() string {
	return filepath.Join(os.TempDir(), "board-tmux-"+strconv.Itoa(os.Getuid())+".sock")
}

// defaultTmux returns a process-wide [gotmux.Tmux] handle bound to the board's
// private socket. The struct is built directly (rather than via
// gotmux.NewTmux) because NewTmux validates the socket eagerly, which fails
// before the first session has started the server. The handle only remembers
// the socket path, so it is safe to reuse across goroutines.
var defaultTmux = sync.OnceValues(func() (*gotmux.Tmux, error) {
	return &gotmux.Tmux{Socket: &gotmux.Socket{Path: SocketPath()}}, nil
})

// serverDefaults are tmux options the board applies to its private server so
// every embedded session feels like a native terminal: no tmux chrome, keys
// passed straight through, full terminal fidelity, and client-driven sizing.
// They are set at global scope: the board owns this server, so global is the
// right place and avoids re-setting them per session.
var serverDefaults = [][]string{
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

	// Input behavior: every keystroke reaches the agent, ESC is instant. With
	// no prefix bound, C-b reaches the agent too, so no unbind is needed.
	// extended-keys forwards CSI-u (Kitty keyboard) sequences so modified keys
	// like C-1, C-2, C-m reach the agent; the board's private server has no
	// user config, so it must enable this explicitly (default is off).
	{"set", "-g", "prefix", "none"},
	{"set", "-g", "prefix2", "none"},
	{"set", "-g", "escape-time", "0"},
	{"set", "-g", "mouse", "on"},
	{"set", "-g", "extended-keys", "always"},

	// Terminal fidelity: truecolor, clipboard, focus events. terminal-features
	// is set (not appended): the board owns the value on its own server, so a
	// plain set keeps it from accumulating duplicates across restarts.
	{"set", "-g", "allow-passthrough", "on"},
	{"set", "-g", "focus-events", "on"},
	{"set", "-g", "set-clipboard", "on"},
	{"set", "-g", "default-terminal", "tmux-256color"},
	{"set", "-g", "terminal-features", ",xterm-256color:clipboard:ccolour:cstyle:extkeys:focus:title:mouse:RGB"},
	{"set-environment", "-g", "LANG", "en_US.UTF-8"},
	{"set-environment", "-g", "LC_ALL", "en_US.UTF-8"},

	// Sizing: follow the attached client, not the smallest one.
	{"set", "-g", "aggressive-resize", "on"},
	{"set", "-g", "window-size", "latest"},
}

// applyServerDefaults applies [serverDefaults] to the board's private server.
func applyServerDefaults() {
	for _, args := range serverDefaults {
		_ = tmuxRun(args...)
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
// git worktree (branched from worktreeBase) and every tool runs inside it. On
// resume, worktreeName is empty and --worktree is omitted: docker agent
// reattaches the session to its original worktree automatically, so passing
// --worktree again (which would fail, the worktree already exists) is avoided.
//
// A non-empty promptFile is delivered as the first message via stdin
// (`- < file`). The prompt never appears on the command line: this command is
// typed into an interactive shell (see NewSession), where no amount of
// escaping is fully safe for arbitrary user text.
func agentCommand(agent, sessionID, listenSocket, worktreeName, worktreeBase, promptFile string) string {
	cmd := fmt.Sprintf("docker agent run %s --yolo --session %s --listen %s",
		agent, shellescape.Quote(sessionID), shellescape.Quote("unix://"+listenSocket))
	if worktreeName != "" {
		cmd += fmt.Sprintf(" --worktree=%s --worktree-base %s",
			shellescape.Quote(worktreeName), shellescape.Quote(worktreeBase))
	}
	if promptFile != "" {
		cmd += " - < " + shellescape.Quote(promptFile)
	}
	return cmd
}

// promptFilePath returns where a session's first prompt is staged for stdin
// delivery. Derived from the (unique) docker-agent session id, so a relaunch
// of the same session overwrites its own file rather than piling up new ones.
func promptFilePath(sessionID string) string {
	return filepath.Join(os.TempDir(), "board-prompt-"+sessionID)
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
func (Sessions) NewSession(sessionName, workDir, agent, sessionID, listenSocket, worktreeName, worktreeBase, prompt string) error {
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

	applyServerDefaults()

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

	// The prompt goes through a file redirected to the agent's stdin, never
	// through the typed command line: send-keys feeds an interactive shell,
	// which would reinterpret any special characters in the prompt.
	promptFile := ""
	if prompt != "" {
		promptFile = promptFilePath(sessionID)
		if err := os.WriteFile(promptFile, []byte(prompt), 0o600); err != nil {
			return fmt.Errorf("write prompt file: %w", err)
		}
	}

	// exec replaces the shell with the agent so the agent becomes the pane's
	// process: when it exits the pane goes dead (see remain-on-exit above).
	if err := panes[0].SendKeys("exec " + agentCommand(agent, sessionID, listenSocket, worktreeName, worktreeBase, promptFile)); err != nil {
		return fmt.Errorf("send keys: %w", err)
	}
	if err := panes[0].SendKeys("Enter"); err != nil {
		return fmt.Errorf("send enter: %w", err)
	}

	return nil
}

// tmuxRun runs `tmux -S <socket> <args...>` against the board's private server
// and returns combined output as part of any error.
func tmuxRun(args ...string) error {
	out, err := exec.Command("tmux", append([]string{"-S", SocketPath()}, args...)...).CombinedOutput()
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
	// No server (after a reboot or a killed server) means every session is
	// gone: report not alive rather than the query error, so the controller
	// relaunches the agents instead of waiting forever.
	if !serverRunning() {
		return false, nil
	}

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

// serverRunning reports whether the board's private tmux server accepts
// connections on its socket. A missing or stale socket fails the dial.
func serverRunning() bool {
	conn, err := net.Dial("unix", SocketPath())
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
