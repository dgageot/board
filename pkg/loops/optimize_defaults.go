package loops

import (
	"context"
	"fmt"
	"strings"
)

// OptimizeDefault uses the selected agent and its most recent project run.
// It shares the same scheduler and quality gates as custom experiments.
func (s *Service) OptimizeDefault(ctx context.Context, loopID, versionID string) (Round, error) {
	detail, err := s.store.Get(ctx, loopID)
	if err != nil {
		return Round{}, err
	}
	if versionID == "" {
		versionID = detail.Loop.ActiveVersion
		if versionID == "" && len(detail.Versions) > 0 {
			versionID = detail.Versions[len(detail.Versions)-1].ID
		}
	}
	jobs, err := s.store.Jobs(ctx, loopID)
	if err != nil {
		return Round{}, err
	}
	input := OptimizeRequest{Version: versionID, ProjectID: detail.Loop.ProjectID, Repeats: 2, Models: []string{"auto", "auto"}}
	for _, job := range jobs {
		if job.Kind == "run" && job.VersionID == versionID && job.Project != nil {
			input.ProjectID = job.Project.ID
			break
		}
	}
	seen := map[string]bool{}
	total := 0
	for _, job := range jobs {
		if job.Kind != "run" || job.VersionID != versionID || job.Project == nil || job.Project.ID != input.ProjectID || strings.TrimSpace(job.Input) == "" || seen[job.Input] {
			continue
		}
		if total+len(job.Input) > 64*1024 {
			continue
		}
		input.Inputs = append(input.Inputs, job.Input)
		seen[job.Input] = true
		total += len(job.Input)
		if len(input.Inputs) == 3 {
			break
		}
	}
	if len(input.Inputs) == 0 {
		input.Inputs = []string{detail.Loop.Description}
	}
	if input.ProjectID == "" {
		return Round{}, fmt.Errorf("%w: run this agent on a project first, or choose a project in custom settings", ErrInvalid)
	}
	return s.Optimize(ctx, loopID, input)
}
