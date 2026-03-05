package board

// SessionManager abstracts tmux session operations.
type SessionManager interface {
	NewSession(name, workDir, agent, prompt string) error
	KillSession(name string) error
	SendKeys(name, message string) error
	PaneContent(name string) (string, error)
}
