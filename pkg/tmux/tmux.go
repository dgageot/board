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

// NewSession creates a tmux session and runs docker agent in it.
func (Sessions) NewSession(sessionName, workDir, agent, prompt string) error {
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

	panes, err := session.ListPanes()
	if err != nil {
		return fmt.Errorf("list panes: %w", err)
	}
	if len(panes) == 0 {
		return errors.New("no panes in session")
	}

	cmd := fmt.Sprintf("docker agent run %s --yolo %s", agent, shellescape.Quote(prompt))
	if err := panes[0].SendKeys(cmd); err != nil {
		return fmt.Errorf("send keys: %w", err)
	}
	if err := panes[0].SendKeys("Enter"); err != nil {
		return fmt.Errorf("send enter: %w", err)
	}

	return nil
}

// SendKeys sends a follow-up message to a running docker agent session.
// A leading Escape dismisses any modal, menu or scroll mode so focus is
// restored to the prompt editor before typing. The message is then sent
// with -l (literal) so it lands in the editor as-is, and Enter submits it.
func (Sessions) SendKeys(sessionName, message string) error {
	if err := tmuxRun("send-keys", "-t", sessionName, "Escape"); err != nil {
		return fmt.Errorf("send-keys Escape: %w", err)
	}
	if err := tmuxRun("send-keys", "-l", "-t", sessionName, message); err != nil {
		return fmt.Errorf("send-keys -l: %w", err)
	}
	if err := tmuxRun("send-keys", "-t", sessionName, "Enter"); err != nil {
		return fmt.Errorf("send-keys Enter: %w", err)
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

// errSessionNotFound indicates the requested tmux session does not exist.
var errSessionNotFound = errors.New("tmux session not found")

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

// PaneContent captures the current content of the first pane in a session.
func (Sessions) PaneContent(sessionName string) (string, error) {
	tmux, err := defaultTmux()
	if err != nil {
		return "", err
	}

	session, err := tmux.GetSessionByName(sessionName)
	if err != nil {
		return "", err
	}
	if session == nil {
		return "", errSessionNotFound
	}

	panes, err := session.ListPanes()
	if err != nil {
		return "", err
	}
	if len(panes) == 0 {
		return "", errors.New("no panes in session")
	}

	return panes[0].CapturePane(nil)
}
