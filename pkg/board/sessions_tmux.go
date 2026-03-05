package board

import "github.com/dgageot/board/pkg/tmux"

// tmuxSessionManager implements SessionManager using the tmux package.
type tmuxSessionManager struct{}

func (tmuxSessionManager) NewSession(name, workDir, agent, prompt string) error {
	return tmux.NewSession(name, workDir, agent, prompt)
}

func (tmuxSessionManager) KillSession(name string) error {
	return tmux.KillSession(name)
}

func (tmuxSessionManager) SendKeys(name, message string) error {
	return tmux.SendKeys(name, message)
}

func (tmuxSessionManager) PaneContent(name string) (string, error) {
	return tmux.PaneContent(name)
}
