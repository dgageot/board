package board

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/dgageot/board/pkg/git"
	"github.com/dgageot/board/pkg/loops"
)

// ProjectTask links an explicitly authorized Board task to its original loop contract.
type ProjectTask struct {
	ID          string `db:"id"           json:"id"`
	LoopID      string `db:"loop_id"      json:"loopId"`
	ProjectID   string `db:"project_id"   json:"projectId"`
	ProjectName string `db:"project_name" json:"projectName"`
	RepoPath    string `db:"repo_path"    json:"repoPath"`
	Agent       string `db:"agent"        json:"agent"`
	Task        string `db:"task"         json:"task"`
	Criteria    string `db:"criteria"     json:"criteria"`
	Prompt      string `db:"prompt"       json:"prompt"`
	CardID      string `db:"card_id"      json:"cardId"`
	Base        string `db:"base"         json:"base"`
	Status      string `db:"status"       json:"status"`
	Error       string `db:"error"        json:"error"`
	CreatedAt   string `db:"created_at"   json:"createdAt"`
	VersionID   string `db:"version_id"   json:"versionId"`
}

type projectTaskStore interface {
	GetProjectTask(id string) (*ProjectTask, error)
	ReserveProjectTask(task *ProjectTask, card *Card) error
	UpdateProjectTask(id, status, message string) error
	ListProjectTasks(loopID string) ([]ProjectTask, error)
	ProjectTaskForCard(cardID string) (*ProjectTask, error)
}

func (b *Board) handleLoopProjectTasks(w http.ResponseWriter, r *http.Request) {
	store, ok := b.store.(projectTaskStore)
	if !ok {
		http.Error(w, "project tasks unavailable", http.StatusServiceUnavailable)
		return
	}
	if _, err := b.loops.Get(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, "loop not found", http.StatusNotFound)
		return
	}
	tasks, err := store.ListProjectTasks(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	type response struct {
		ProjectTask

		Card   *Card      `json:"card,omitempty"`
		Review *RunReview `json:"review,omitempty"`
	}
	result := []response{}
	for _, task := range tasks {
		card, err := b.store.GetCard(task.CardID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeError(w, err)
			return
		}
		var review *RunReview
		if reviews, ok := b.store.(reviewStore); ok {
			if saved, err := reviews.RunReview(task.ID); err == nil {
				saved.Events = nil
				saved.Usage = nil
				saved.OpenStreams = nil
				saved.SeenSessions = nil
				saved.Background = nil
				review = &saved
			} else if !errors.Is(err, sql.ErrNoRows) {
				writeError(w, err)
				return
			}
		}
		result = append(result, response{ProjectTask: task, Card: card, Review: review})
	}
	writeJSON(w, result)
}

func (b *Board) handleRunLoopProjectTask(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RequestID string `json:"requestId"`
		VersionID string `json:"versionId"`
		ProjectID string `json:"projectId"`
		Agent     string `json:"agent"`
		RepoPath  string `json:"repoPath"`
		Confirmed bool   `json:"confirmed"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Confirmed || input.RequestID == "" || len(input.RequestID) > 128 || input.ProjectID == "" {
		writeError(w, fmt.Errorf("%w: explicit project-task consent and a request ID are required", errBadInput))
		return
	}
	store, ok := b.store.(projectTaskStore)
	if !ok {
		http.Error(w, "project tasks unavailable", http.StatusServiceUnavailable)
		return
	}
	b.taskLaunch.Lock()
	defer b.taskLaunch.Unlock()
	if existing, err := store.GetProjectTask(input.RequestID); err == nil {
		if existing.LoopID != r.PathValue("id") || existing.ProjectID != input.ProjectID || existing.VersionID != input.VersionID {
			http.Error(w, "request ID already used", http.StatusConflict)
			return
		}
		writeJSON(w, existing)
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeError(w, err)
		return
	}
	detail, err := b.loops.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "loop not found", http.StatusNotFound)
		return
	}
	project, err := b.store.GetProject(input.ProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, fmt.Errorf("%w: project not found", errBadInput))
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if project.Agent != input.Agent || project.RepoPath != input.RepoPath {
		http.Error(w, "project changed; reopen the dialog to review its agent and path", http.StatusConflict)
		return
	}
	path, err := filepath.Abs(project.RepoPath)
	if err != nil {
		writeError(w, err)
		return
	}
	if strings.TrimSpace(project.Agent) == "" || !git.IsRepo(path) {
		writeError(w, fmt.Errorf("%w: project requires an agent and an accessible Git repository", errBadInput))
		return
	}
	instruction, err := projectWorkflowInstruction(detail, input.VersionID)
	if err != nil {
		writeError(w, err)
		return
	}
	base, err := git.ExecutionBase(r.Context(), path)
	if err != nil {
		writeError(w, fmt.Errorf("%w: %w", errBadInput, err))
		return
	}
	prompt := fmt.Sprintf("Task:\n%s\n\nAcceptance criteria:\n%s\n\nWork in this task's isolated worktree. Perform the actual work, not a simulated report. Never claim tests or CI passed without observing their results. If signing, credentials, required tools or CI access are unavailable, report the blocker. All commits must be signed; never disable signing. Follow repository instructions. Do not push or create a PR unless the task explicitly requests it or the user approves it interactively.\n", detail.Loop.Description, detail.Loop.Criteria)
	if instruction != "" {
		prompt += "\nWorkflow instruction:\n" + instruction
	}
	name := newWorktreeName()
	card := &Card{ID: newID(), Title: detail.Loop.Name, Prompt: prompt, Status: StatusStarting, Project: project.Name, Agent: project.Agent, RepoPath: path, Branch: git.WorktreeBranch(name), Worktree: git.WorktreeDir(name), Session: newSessionName(), AgentSession: newAgentSessionID()}
	task := &ProjectTask{ID: input.RequestID, LoopID: detail.Loop.ID, ProjectID: project.ID, ProjectName: project.Name, RepoPath: path, Agent: project.Agent, Task: detail.Loop.Description, Criteria: detail.Loop.Criteria, Prompt: prompt, CardID: card.ID, Base: base, VersionID: input.VersionID, Status: "reserved", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.ReserveProjectTask(task, card); err != nil {
		writeError(w, err)
		return
	}
	// A reservation is never automatically replayed, including after an ambiguous launch.
	err = b.sessions.NewSession(card.Session, path, card.Agent, card.AgentSession, socketPath(card.AgentSession), name, task.Base, prompt)
	// Watch even an ambiguous launch: command delivery can fail after execution began.
	b.controller.ExpectTurn(card.ID)
	b.controller.Start(card)
	b.broadcast()
	if err != nil {
		task.Status, task.Error = "launch_uncertain", err.Error()
		_ = store.UpdateProjectTask(task.ID, task.Status, task.Error)
		_ = b.store.UpdateCardStatus(card.ID, StatusError)
		b.broadcast()
		writeJSONStatus(w, http.StatusAccepted, task)
		return
	}
	if err := store.UpdateProjectTask(task.ID, "launched", ""); err != nil {
		writeError(w, err)
		return
	}
	task.Status = "launched"
	b.broadcast()
	writeJSONStatus(w, http.StatusAccepted, task)
}

func projectWorkflowInstruction(detail loops.Detail, versionID string) (string, error) {
	if detail.Loop.Mode != loops.ModeProject {
		return "", fmt.Errorf("%w: switch this loop to project execution first", errBadInput)
	}
	if versionID == "" {
		return "", fmt.Errorf("%w: generate a project workflow first", errBadInput)
	}
	for _, version := range detail.Versions {
		if version.ID != versionID {
			continue
		}
		if version.Mode != loops.ModeProject {
			return "", fmt.Errorf("%w: choose a generated project workflow", errBadInput)
		}
		instruction, err := loops.ProjectInstruction(version)
		if err != nil {
			return "", fmt.Errorf("%w: %w", errBadInput, err)
		}
		return instruction, nil
	}
	return "", fmt.Errorf("%w: workflow version not found", errBadInput)
}
