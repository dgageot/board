package board

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
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
	{ID: "push", Name: "Push", Emoji: "🚀", Prompt: "Squash all commits on this branch into a single commit with a clear and concise commit message. Push the branch to my fork (remote: dgageot). Then use gh to open a pull request."},
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
	Auto     bool       `json:"auto" db:"auto"`
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

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// sanitizeBranch creates a safe branch name from a title with a short UUID suffix to avoid conflicts.
func sanitizeBranch(title string) string {
	s := strings.ToLower(title)
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, s)
	// Collapse multiple dashes
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return "board/" + s + "-" + newID()[:8]
}
