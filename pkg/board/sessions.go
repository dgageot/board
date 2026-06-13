package board

// SessionManager abstracts tmux session operations. The board runs each card's
// agent in a tmux session (so the user can attach an interactive terminal) and
// observes/drives it through its control plane (see [Controller]); these are
// the few tmux operations that remain.
type SessionManager interface {
	// NewSession creates a tmux session running the docker agent for the given
	// docker-agent session ID, from workDir. The agent exposes its control
	// plane on listenSocket (--listen unix://). A non-empty worktreeName marks
	// the first run: docker agent creates an isolated worktree of that name and
	// workDir is the repository. On resume, worktreeName is empty and workDir is
	// the worktree directory. A non-empty prompt is sent as the first message.
	// The agent is the pane's process and the session is kept (as a dead pane)
	// when the agent exits, so the user can read its final output.
	NewSession(name, workDir, agent, sessionID, listenSocket, worktreeName, prompt string) error
	KillSession(name string) error
	// Alive reports whether the session exists and its agent pane is still
	// running. It lets the controller tell a control plane that is merely slow
	// to start from a session whose agent has died and must be relaunched.
	Alive(name string) (bool, error)
}
