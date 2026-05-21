package board

import (
	"crypto/rand"
	"encoding/hex"
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
	{ID: "push", Name: "Push", Emoji: "🚀", Prompt: "Start by committing any remaining uncommitted files. Then rebase on top of origin/main and fix any test failures and linter issues. Finally, squash all commits on this branch into a single commit with a clear and concise commit message. Push the branch to my fork (remote: dgageot). Then use gh to open a pull request."},
	{ID: "done", Name: "Done", Emoji: "✅", Prompt: ""},
}

// CardStatus tracks whether a card is running or idle.
type CardStatus string

const (
	StatusRunning CardStatus = "running"
	StatusWaiting CardStatus = "waiting"
	StatusDone    CardStatus = "done"
)

// Card represents a task card on the board.
type Card struct {
	ID       string     `json:"id" db:"id"`
	Title    string     `json:"title" db:"title"`
	Column   string     `json:"column" db:"col"`
	Status   CardStatus `json:"status" db:"status"`
	Agent    string     `json:"agent" db:"agent"`
	RepoPath string     `json:"repoPath" db:"repo_path"`
	Branch   string     `json:"branch" db:"branch"`
	Worktree string     `json:"worktree" db:"worktree"`
	Session  string     `json:"session" db:"session"`
}

// Project represents a saved project config.
type Project struct {
	ID       string `json:"id" db:"id"`
	Name     string `json:"name" db:"name"`
	RepoPath string `json:"repoPath" db:"repo_path"`
	Agent    string `json:"agent" db:"agent"`
}

// branchPrefix is used for all worktree branches created by the board.
const branchPrefix = "board/"

// sessionPrefix is used for all tmux sessions created by the board.
const sessionPrefix = "board-"

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newBranchName returns a unique branch name for a local card. The name
// is random rather than derived from the card's title, so the worktree
// directory does not depend on the (slower) title generation.
func newBranchName() string {
	return branchPrefix + newID()
}

// newSessionName returns a unique tmux session name for a card.
func newSessionName() string {
	return sessionPrefix + newID()[:8]
}
