package board

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
)

// Column represents a kanban column with a pre-defined prompt.
type Column struct {
	ID     string `json:"id" db:"id"`
	Name   string `json:"name" db:"name"`
	Emoji  string `json:"emoji" db:"emoji"`
	Prompt string `json:"prompt" db:"prompt"`
}

var defaultColumns = []Column{
	{ID: "dev", Name: "Dev", Emoji: "🔨", Prompt: ""},
	{ID: "simplify", Name: "Simplify", Emoji: "✨", Prompt: "Start by committing any local changes. Then look at these changes and try to simplify the code and architecture but don't remove any feature. I just want the code to be easier to read and maintain."},
	{ID: "review", Name: "Review", Emoji: "🔍", Prompt: "Review the local changes. Look for bugs, security issues, and code quality problems. Fix any issues you find."},
	{ID: "fix", Name: "Fix", Emoji: "🔧", Prompt: "Run the linter and fix any lint issues. Run the tests and fix any failing tests."},
	{ID: "push", Name: "Push", Emoji: "🚀", Prompt: "Start by committing any remaining uncommitted files. Then rebase on top of the upstream default branch and fix any test failures and linter issues. Finally, squash all commits on this branch into a single commit with a clear and concise commit message. Push the branch to your fork (or the appropriate remote). Then use gh to open a pull request."},
	{ID: "done", Name: "Done", Emoji: "✅", Prompt: ""},
}

// CardStatus tracks whether a card is running or idle.
type CardStatus string

const (
	StatusRunning CardStatus = "running"
	StatusWaiting CardStatus = "waiting"
	// StatusError marks a card whose last turn failed. It is sticky: the
	// watcher keeps it until the next turn starts (stream_started).
	StatusError CardStatus = "error"
)

// Card represents a task card on the board.
type Card struct {
	ID       string     `json:"id" db:"id"`
	Title    string     `json:"title" db:"title"`
	Column   string     `json:"column" db:"col"`
	Status   CardStatus `json:"status" db:"status"`
	Project  string     `json:"project" db:"project"`
	Agent    string     `json:"agent" db:"agent"`
	RepoPath string     `json:"repoPath" db:"repo_path"`
	Branch   string     `json:"branch" db:"branch"`
	Worktree string     `json:"worktree" db:"worktree"`
	Session  string     `json:"session" db:"session"`
	// AgentSession is the docker-agent conversation ID the card owns. It is
	// passed to `docker agent run --session` on every launch, so a session
	// recreated after the agent (or tmux) dies resumes the same conversation
	// instead of starting over.
	AgentSession string `json:"agentSession" db:"agent_session"`
}

// Project represents a saved project config.
type Project struct {
	ID       string `json:"id" db:"id"`
	Name     string `json:"name" db:"name"`
	RepoPath string `json:"repoPath" db:"repo_path"`
	Agent    string `json:"agent" db:"agent"`
	Pos      int    `json:"pos" db:"pos"`
}

// sessionPrefix is used for all tmux sessions created by the board.
const sessionPrefix = "board-"

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newWorktreeName returns a unique worktree name for a card. docker-agent
// derives the worktree directory (~/.cagent/worktrees/<name>) and branch
// (worktree-<name>) from it, so the name must be a single path segment: a
// plain hex id with a "board-" prefix. It is random rather than derived from
// the card's title, so worktree creation does not wait on title generation.
func newWorktreeName() string {
	return "board-" + newID()
}

// newSessionName returns a unique tmux session name for a card.
func newSessionName() string {
	return sessionPrefix + newID()[:8]
}

// newAgentSessionID returns a unique docker-agent session ID for a card.
func newAgentSessionID() string {
	return newID()
}

// socketPath returns the unix socket a card's agent control plane listens on.
// It is derived from the (unique) docker-agent session id, so it is stable
// across board restarts and needs no extra storage. Kept short to stay under
// the ~104-byte unix sun_path limit.
func socketPath(agentSession string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cagent", "run", "board-"+agentSession+".sock")
}
