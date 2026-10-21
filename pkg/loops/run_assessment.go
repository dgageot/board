package loops

import (
	"errors"
	"slices"
	"strings"
)

// RunAssessment is a judge opinion, not a substitute for observed tests or CI.
type RunAssessment struct {
	Quality      string   `json:"quality"`
	Efficiency   string   `json:"efficiency"`
	ChangeImpact string   `json:"changeImpact"`
	Summary      string   `json:"summary"`
	Improvements []string `json:"improvements"`
}

func (a RunAssessment) Validate() error {
	if !slices.Contains([]string{"satisfied", "failed", "unknown"}, a.Quality) || !slices.Contains([]string{"efficient", "mixed", "inefficient", "unknown"}, a.Efficiency) || !slices.Contains([]string{"helped", "hurt", "inconclusive"}, a.ChangeImpact) || strings.TrimSpace(a.Summary) == "" || len(a.Summary) > 8000 || len(a.Improvements) > 8 {
		return errors.New("invalid run assessment")
	}
	for _, item := range a.Improvements {
		if len(item) > 2000 {
			return errors.New("assessment suggestion too long")
		}
	}
	return nil
}
