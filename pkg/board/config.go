package board

import (
	"cmp"
	"os"
)

// Config holds the application configuration.
type Config struct {
	ListenAddr    string
	EditorCommand string
	// CoachAgent is the agent config the harness coach runs. Empty means the
	// coach embedded in the board binary (see coach.yaml).
	CoachAgent string
}

// DefaultConfig returns a Config populated from environment variables with sensible defaults.
func DefaultConfig() Config {
	return Config{
		ListenAddr:    cmp.Or(os.Getenv("BOARD_ADDR"), "localhost:8077"),
		EditorCommand: cmp.Or(os.Getenv("BOARD_EDITOR"), "code"),
		CoachAgent:    os.Getenv("BOARD_COACH_AGENT"),
	}
}
