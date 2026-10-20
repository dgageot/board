// Package loops manages versioned agents and their optimization experiments.
package loops

import "errors"

var (
	ErrNotFound = errors.New("loop resource not found")
	ErrInvalid  = errors.New("invalid loop input")
	ErrConflict = errors.New("loop changed; refresh and try again")
)

// Loop is a task contract, independent of any particular agent version.
type Loop struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Criteria      string `json:"criteria"`
	Example       string `json:"example"`
	ActiveVersion string `json:"activeVersion"`
	CreatedAt     string `json:"createdAt"`
}

// Version is an immutable, self-contained agent configuration.
type Version struct {
	ID        string `json:"id"`
	LoopID    string `json:"loopId"`
	Number    int    `json:"number"`
	YAML      string `json:"yaml"`
	Rationale string `json:"rationale"`
	CreatedAt string `json:"createdAt"`
}

// Detail is the snapshot displayed by the loop inspector.
type Detail struct {
	Loop     Loop      `json:"loop"`
	Versions []Version `json:"versions"`
}

// Job is a durable generation or execution attempt; interrupted attempts never resume silently.
type Job struct {
	ID         string  `json:"id"`
	LoopID     string  `json:"loopId"`
	VersionID  string  `json:"versionId"`
	Kind       string  `json:"kind"`
	Status     string  `json:"status"`
	Input      string  `json:"input"`
	Output     string  `json:"output"`
	Error      string  `json:"error"`
	Metrics    Metrics `json:"metrics"`
	CreatedAt  string  `json:"createdAt"`
	FinishedAt string  `json:"finishedAt"`
}

// Metrics separates process wall time from active inference and preserves unknown pricing.
type Metrics struct {
	Cost         *float64 `json:"cost"`
	InputTokens  int64    `json:"inputTokens"`
	OutputTokens int64    `json:"outputTokens"`
	DurationMS   int64    `json:"durationMs"`
	ModelMS      int64    `json:"modelMs"`
	Model        string   `json:"model"`
	Calls        int      `json:"calls"`
}
