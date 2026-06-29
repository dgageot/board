package board

import (
	"cmp"
	"os"
)

// Config holds the application configuration.
type Config struct {
	ListenAddr    string
	EditorCommand string
}

// DefaultConfig returns a Config populated from environment variables with sensible defaults.
func DefaultConfig() Config {
	return Config{
		ListenAddr:    cmp.Or(os.Getenv("BOARD_ADDR"), "localhost:8077"),
		EditorCommand: cmp.Or(os.Getenv("BOARD_EDITOR"), "code"),
	}
}
