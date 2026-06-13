package board

// SessionManager abstracts tmux session operations.
type SessionManager interface {
	// NewSession creates a tmux session running the docker agent for the given
	// docker-agent session ID. A non-empty prompt is sent as the first message;
	// an empty prompt resumes the session and waits for input. The agent is the
	// pane's process and the session is kept (as a dead pane) when the agent
	// exits, so the poller can detect the death and reconnect.
	NewSession(name, workDir, agent, sessionID, prompt string) error
	KillSession(name string) error
	SendKeys(name, message string) error
	// PaneContent returns the pane content and whether the agent pane has died
	// (exited while the session is kept alive). A missing session is reported as
	// an error.
	PaneContent(name string) (content string, dead bool, err error)
}
