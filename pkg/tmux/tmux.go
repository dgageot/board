package tmux

import (
	"errors"
	"fmt"
	"os/exec"

	"al.essio.dev/pkg/shellescape"
	"github.com/GianlucaP106/gotmux/gotmux"
)

// Sessions provides session management operations backed by tmux.
type Sessions struct{}

// NewSession creates a tmux session and runs docker agent in it.
func (Sessions) NewSession(sessionName, workDir, agent, prompt string) error {
	tmux, err := gotmux.DefaultTmux()
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

	// Enable features for better TUI passthrough.
	for _, args := range [][]string{
		{"set", "-g", "allow-passthrough", "on"},
		{"set", "-g", "mouse", "on"},
		{"set", "-g", "default-terminal", "tmux-256color"},
		{"set", "-ga", "terminal-features", ",xterm-256color:clipboard:ccolour:cstyle:focus:title:mouse:RGB"},
		{"set-environment", "LANG", "en_US.UTF-8"},
		{"set-environment", "LC_ALL", "en_US.UTF-8"},
	} {
		cmd := exec.Command("tmux", append([]string{"-t", sessionName}, args...)...)
		_ = cmd.Run()
	}

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
// It uses -l (literal) so the text is typed into the TUI as-is,
// then sends Enter separately to submit it.
func (Sessions) SendKeys(sessionName, message string) error {
	cmd := exec.Command("tmux", "send-keys", "-l", "-t", sessionName, message)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("send-keys -l: %s: %w", out, err)
	}

	cmd = exec.Command("tmux", "send-keys", "-t", sessionName, "Enter")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("send-keys Enter: %s: %w", out, err)
	}

	return nil
}

// KillSession kills a tmux session.
func (Sessions) KillSession(sessionName string) error {
	tmux, err := gotmux.DefaultTmux()
	if err != nil {
		return err
	}

	session, err := tmux.GetSessionByName(sessionName)
	if err != nil {
		return nil // session doesn't exist, nothing to kill
	}

	return session.Kill()
}

// PaneContent captures the current content of the first pane in a session.
func (Sessions) PaneContent(sessionName string) (string, error) {
	tmux, err := gotmux.DefaultTmux()
	if err != nil {
		return "", err
	}

	session, err := tmux.GetSessionByName(sessionName)
	if err != nil {
		return "", err
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
