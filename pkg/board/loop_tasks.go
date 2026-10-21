package board

import (
	"context"
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

// ProjectTask links a user-started Board task to its original loop contract.
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

type loopTaskRequest struct {
	RequestID     string `json:"requestId"`
	VersionID     string `json:"versionId"`
	ProjectID     string `json:"projectId"`
	Agent         string `json:"agent"`
	RepoPath      string `json:"repoPath"`
	executionPath string
}

func (b *Board) handleRunLoopProjectTask(w http.ResponseWriter, r *http.Request) {
	var input loopTaskRequest
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	if !decodeJSON(w, r, &input) {
		return
	}
	b.taskLaunch.Lock()
	defer b.taskLaunch.Unlock()
	task, status, err := b.runLoopProjectTaskLocked(r.Context(), r.PathValue("id"), input)
	if err != nil {
		if status != 0 {
			http.Error(w, err.Error(), status)
		} else {
			writeError(w, err)
		}
		return
	}
	writeJSONStatus(w, status, task)
}

// The caller holds taskLaunch through reservation and launch, including automatic runs.
func (b *Board) runLoopProjectTaskLocked(ctx context.Context, loopID string, input loopTaskRequest) (*ProjectTask, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if input.RequestID == "" || len(input.RequestID) > 128 || input.ProjectID == "" {
		return nil, 0, fmt.Errorf("%w: a project and request ID are required", errBadInput)
	}
	store, ok := b.store.(projectTaskStore)
	if !ok {
		return nil, http.StatusServiceUnavailable, errors.New("project tasks unavailable")
	}
	getTask := store.GetProjectTask
	getProject := b.store.GetProject
	reserve := store.ReserveProjectTask
	if sqlite, ok := store.(*SQLiteStore); ok {
		getTask = func(id string) (*ProjectTask, error) {
			var task ProjectTask
			err := sqlite.db.GetContext(ctx, &task, "SELECT "+projectTaskColumns+" FROM loop_project_tasks WHERE id=?", id)
			return &task, err
		}
		getProject = func(id string) (*Project, error) {
			var project Project
			err := sqlite.db.GetContext(ctx, &project, "SELECT "+projectColumns+" FROM projects WHERE id=?", id)
			return &project, err
		}
		reserve = func(task *ProjectTask, card *Card) error { return sqlite.reserveProjectTask(ctx, task, card) }
	}
	if existing, err := getTask(input.RequestID); err == nil {
		if existing.LoopID != loopID || existing.ProjectID != input.ProjectID || existing.VersionID != input.VersionID {
			return nil, http.StatusConflict, errors.New("request ID already used")
		}
		return existing, http.StatusOK, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, 0, err
	}
	detail, err := b.loops.Get(ctx, loopID)
	if err != nil {
		return nil, http.StatusNotFound, errors.New("loop not found")
	}
	project, err := getProject(input.ProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, fmt.Errorf("%w: project not found", errBadInput)
	}
	if err != nil {
		return nil, 0, err
	}
	if project.Agent != input.Agent || project.RepoPath != input.RepoPath {
		return nil, http.StatusConflict, errors.New("project changed; reopen the dialog to review its agent and path")
	}
	path := input.executionPath
	if path == "" {
		path, err = filepath.Abs(project.RepoPath)
	}
	if err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(project.Agent) == "" || !git.IsRepoContext(ctx, path) {
		return nil, 0, fmt.Errorf("%w: project requires an agent and an accessible Git repository", errBadInput)
	}
	instruction, err := projectWorkflowInstruction(detail, input.VersionID)
	if err != nil {
		return nil, 0, err
	}
	base, err := git.ExecutionBase(ctx, path)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", errBadInput, err)
	}
	prompt := fmt.Sprintf("Task:\n%s\n\nAcceptance criteria:\n%s\n\nWork in this task's isolated worktree. Perform the actual work, not a simulated report. Never claim tests or CI passed without observing their results. If signing, credentials, required tools or CI access are unavailable, report the blocker. All commits must be signed; never disable signing. Follow repository instructions. Do not push or create a PR unless the task explicitly requests it or the user approves it interactively.\n", detail.Loop.Description, detail.Loop.Criteria)
	if instruction != "" {
		prompt += "\nWorkflow instruction:\n" + instruction
	}
	name := newWorktreeName()
	card := &Card{ID: newID(), Title: detail.Loop.Name, Prompt: prompt, Status: StatusStarting, Project: project.Name, Agent: project.Agent, RepoPath: path, Branch: git.WorktreeBranch(name), Worktree: git.WorktreeDir(name), Session: newSessionName(), AgentSession: newAgentSessionID()}
	task := &ProjectTask{ID: input.RequestID, LoopID: detail.Loop.ID, ProjectID: project.ID, ProjectName: project.Name, RepoPath: path, Agent: project.Agent, Task: detail.Loop.Description, Criteria: detail.Loop.Criteria, Prompt: prompt, CardID: card.ID, Base: base, VersionID: input.VersionID, Status: "reserved", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if err := reserve(task, card); err != nil {
		return nil, 0, err
	}
	// A reservation is never automatically replayed, including after an ambiguous launch.
	if sessions, ok := b.sessions.(interface {
		NewSessionContext(ctx context.Context, name, workDir, agent, sessionID, listenSocket, worktreeName, worktreeBase, prompt string) error
	}); ok {
		err = sessions.NewSessionContext(ctx, card.Session, path, card.Agent, card.AgentSession, socketPath(card.AgentSession), name, task.Base, prompt)
	} else {
		err = b.sessions.NewSession(card.Session, path, card.Agent, card.AgentSession, socketPath(card.AgentSession), name, task.Base, prompt)
	}
	// Retain an ambiguous delivery; neither cancellation nor a storage failure permits replay.
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer finishCancel()
	launchErr := err
	task.Status = "launched"
	if launchErr != nil {
		task.Status = "launch_uncertain"
		task.Error = launchErr.Error()
	}
	var saveErr error
	if sqlite, ok := store.(*SQLiteStore); ok {
		_, saveErr = sqlite.db.ExecContext(finishCtx, `UPDATE loop_project_tasks SET status=?,error=? WHERE id=?`, task.Status, task.Error, task.ID)
		if launchErr != nil {
			_, cardErr := sqlite.db.ExecContext(finishCtx, `UPDATE cards SET status=? WHERE id=?`, StatusError, card.ID)
			saveErr = errors.Join(saveErr, cardErr)
		}
	} else {
		saveErr = store.UpdateProjectTask(task.ID, task.Status, task.Error)
		if launchErr != nil {
			saveErr = errors.Join(saveErr, b.store.UpdateCardStatus(card.ID, StatusError))
		}
	}
	b.controller.ExpectTurn(card.ID)
	b.controller.startWatcher(card)
	b.broadcast()
	if saveErr != nil {
		return nil, 0, fmt.Errorf("persist launch outcome: %w", errors.Join(launchErr, saveErr))
	}
	return task, http.StatusAccepted, nil
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
