package board

import (
	"cmp"
	"os"
)

// Config holds the application configuration.
type Config struct {
	DefaultAgent    string
	DefaultRepoPath string
	ListenAddr      string
}

// DefaultConfig returns a Config populated from environment variables with sensible defaults.
func DefaultConfig() Config {
	return Config{
		DefaultAgent:    cmp.Or(os.Getenv("BOARD_DEFAULT_AGENT"), "agent.yaml"),
		DefaultRepoPath: cmp.Or(os.Getenv("BOARD_DEFAULT_REPO"), "."),
		ListenAddr:      cmp.Or(os.Getenv("BOARD_ADDR"), ":8077"),
	}
}
