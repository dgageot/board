package tmux

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/GianlucaP106/gotmux/gotmux"
)

// shellQuote wraps s in single quotes so that the shell passes it
// through verbatim (no expansion of $, `, !, etc.).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// NewSession creates a tmux session and runs docker agent in it.
func NewSession(sessionName, workDir, agent, prompt string) error {
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
	for _, opt := range []string{
		"set -g allow-passthrough on",
		"set -g mouse on",
		"set -g default-terminal tmux-256color",
		"set -ga terminal-features ',xterm-256color:clipboard:ccolour:cstyle:focus:title:mouse:RGB'",
	} {
		cmd := exec.Command("tmux", append([]string{"-t", sessionName}, strings.Fields(opt)...)...)
		_ = cmd.Run()
	}

	// Set UTF-8 locale so TUI apps can render special characters.
	for _, env := range []string{
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
	} {
		k, v, _ := strings.Cut(env, "=")
		cmd := exec.Command("tmux", "set-environment", "-t", sessionName, k, v)
		_ = cmd.Run()
	}

	windows, err := session.ListWindows()
	if err != nil {
		return fmt.Errorf("list windows: %w", err)
	}
	if len(windows) == 0 {
		return fmt.Errorf("no windows in session")
	}

	panes, err := windows[0].ListPanes()
	if err != nil {
		return fmt.Errorf("list panes: %w", err)
	}
	if len(panes) == 0 {
		return fmt.Errorf("no panes in window")
	}

	cmd := fmt.Sprintf("docker agent run %s --yolo %s", agent, shellQuote(prompt))
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
func SendKeys(sessionName, message string) error {
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
func KillSession(sessionName string) error {
	tmux, err := gotmux.DefaultTmux()
	if err != nil {
		return err
	}

	if !tmux.HasSession(sessionName) {
		return nil
	}

	session, err := tmux.GetSessionByName(sessionName)
	if err != nil {
		return err
	}

	return session.Kill()
}

// PaneContent captures the current content of the first pane in a session.
func PaneContent(sessionName string) (string, error) {
	tmux, err := gotmux.DefaultTmux()
	if err != nil {
		return "", err
	}

	if !tmux.HasSession(sessionName) {
		return "", fmt.Errorf("session %s not found", sessionName)
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
		return "", fmt.Errorf("no panes in session")
	}

	return panes[0].CapturePane(nil)
}
