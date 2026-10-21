package board

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dgageot/board/pkg/loops"
)

type initialRunner struct {
	run func(context.Context, loops.Request) (loops.Result, error)
}

func (r initialRunner) Run(ctx context.Context, request loops.Request, _ func(loops.Result)) (loops.Result, error) {
	return r.run(ctx, request)
}

func initialRunFixture(t *testing.T, run func(context.Context, loops.Request) (loops.Result, error)) (*Board, *SQLiteStore, *Project, *taskSessions) {
	t.Helper()
	b, store, _, project := taskFixture(t)
	service, err := loops.NewService(t.Context(), b.loops, initialRunner{run: run}, "auto", func() {})
	require.NoError(t, err)
	t.Cleanup(service.Close)
	b.loopService = service
	sessions := &taskSessions{launch: func(string, string) error { return nil }}
	b.sessions = sessions
	t.Cleanup(func() {
		cards, err := store.ListCards()
		if err == nil {
			for _, card := range cards {
				b.controller.Stop(card.ID)
			}
		}
	})
	return b, store, project, sessions
}

func generatedInitialAgent(context.Context, loops.Request) (loops.Result, error) {
	return loops.Result{Output: `{"instruction":"Execute the dependency update and verify tests.","model":"auto","rationale":"Initial task agent"}`}, nil
}

func createInitialLoop(t *testing.T, b *Board, project *Project) loops.Loop {
	t.Helper()
	loop, err := b.loopService.Create(t.Context(), loops.Loop{Name: "First run", Description: "Update dependencies", Criteria: "Tests pass", ProjectID: project.ID, Mode: loops.ModeProject})
	require.NoError(t, err)
	return loop
}

func TestCreateLoopGeneratesThenRunsWithoutBrowser(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	b, store, project, sessions := initialRunFixture(t, func(ctx context.Context, request loops.Request) (loops.Result, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return loops.Result{}, ctx.Err()
		}
		return generatedInitialAgent(ctx, request)
	})
	mux, err := buildMux(b)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/loops", strings.NewReader(`{"name":"First run","description":"Update dependencies","criteria":"Tests pass","projectId":"`+project.ID+`"}`)))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var loop loops.Loop
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &loop))
	<-started
	require.NoError(t, b.processInitialRun(t.Context()))
	assert.Zero(t, sessions.calls.Load())
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	require.NotNil(t, detail.InitialRun)
	assert.Equal(t, "waiting", detail.InitialRun.State)
	close(release)
	b.loopService.Wait()
	stop := b.startInitialRuns(t.Context())
	defer stop()
	require.Eventually(t, func() bool { return sessions.calls.Load() == 1 }, 3*time.Second, 10*time.Millisecond)
	stop()
	detail, err = b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	require.Len(t, detail.Versions, 1)
	assert.Equal(t, "admitted", detail.InitialRun.State)
	assert.Equal(t, detail.Versions[0].ID, detail.Loop.ActiveVersion)
	task, err := store.GetProjectTask(detail.InitialRun.TaskID)
	require.NoError(t, err)
	assert.Equal(t, "launched", task.Status)
	assert.Equal(t, detail.Versions[0].ID, task.VersionID)
	assert.Equal(t, project.ID, task.ProjectID)
	assert.Contains(t, task.Prompt, "Execute the dependency update and verify tests.")
	review, err := store.RunReview(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "observing", review.State)
	require.NoError(t, b.processInitialRun(t.Context()))
	assert.EqualValues(t, 1, sessions.calls.Load())
}

func TestFailedGenerationDoesNotLaunchInitialTask(t *testing.T) {
	b, _, project, sessions := initialRunFixture(t, func(context.Context, loops.Request) (loops.Result, error) {
		return loops.Result{}, errors.New("provider unavailable")
	})
	loop := createInitialLoop(t, b, project)
	b.loopService.Wait()
	require.NoError(t, b.processInitialRun(t.Context()))
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", detail.InitialRun.State)
	assert.Contains(t, detail.InitialRun.Error, "provider unavailable")
	assert.Zero(t, sessions.calls.Load())
}

func TestInitialTaskLaunchFailureIsNotReplayed(t *testing.T) {
	b, store, project, sessions := initialRunFixture(t, generatedInitialAgent)
	sessions.launch = func(string, string) error { return errors.New("ambiguous delivery") }
	loop := createInitialLoop(t, b, project)
	b.loopService.Wait()
	require.NoError(t, b.processInitialRun(t.Context()))
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Equal(t, "admitted", detail.InitialRun.State)
	task, err := store.GetProjectTask(detail.InitialRun.TaskID)
	require.NoError(t, err)
	assert.Equal(t, "launch_uncertain", task.Status)
	require.NoError(t, b.processInitialRun(t.Context()))
	assert.EqualValues(t, 1, sessions.calls.Load())
}

func TestConcurrentInitialWorkersLaunchOnce(t *testing.T) {
	b, _, project, sessions := initialRunFixture(t, generatedInitialAgent)
	_ = createInitialLoop(t, b, project)
	b.loopService.Wait()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { results <- b.processInitialRun(t.Context()) })
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, sessions.calls.Load())
}

func TestManualRunSupersedesPendingInitialRun(t *testing.T) {
	b, _, project, sessions := initialRunFixture(t, generatedInitialAgent)
	loop := createInitialLoop(t, b, project)
	b.loopService.Wait()
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	b.taskLaunch.Lock()
	_, _, err = b.runLoopProjectTaskLocked(t.Context(), loop.ID, loopTaskRequest{RequestID: "manual", ProjectID: project.ID, VersionID: detail.Versions[0].ID, RepoPath: project.RepoPath, Agent: project.Agent})
	b.taskLaunch.Unlock()
	require.NoError(t, err)
	require.NoError(t, b.processInitialRun(t.Context()))
	detail, err = b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Equal(t, "superseded", detail.InitialRun.State)
	assert.EqualValues(t, 1, sessions.calls.Load())
}

func TestInitialRunUsesCapturedGenerationNotLatestVersion(t *testing.T) {
	b, store, project, sessions := initialRunFixture(t, generatedInitialAgent)
	loop := createInitialLoop(t, b, project)
	b.loopService.Wait()
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	first := detail.Versions[0].ID
	_, err = b.loopService.Generate(t.Context(), loop.ID)
	require.NoError(t, err)
	b.loopService.Wait()
	require.NoError(t, b.processInitialRun(t.Context()))
	tasks, err := store.ListProjectTasks(loop.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, first, tasks[0].VersionID)
	assert.EqualValues(t, 1, sessions.calls.Load())
}

func TestInitialRunRejectsChangedProject(t *testing.T) {
	b, store, project, sessions := initialRunFixture(t, generatedInitialAgent)
	loop := createInitialLoop(t, b, project)
	b.loopService.Wait()
	_, err := store.db.Exec(`UPDATE projects SET agent='changed-agent' WHERE id=?`, project.ID)
	require.NoError(t, err)
	require.NoError(t, b.processInitialRun(t.Context()))
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", detail.InitialRun.State)
	assert.Contains(t, detail.InitialRun.Error, "project changed")
	assert.Zero(t, sessions.calls.Load())
}

func TestInitialRunResumesOnlyBeforeReservation(t *testing.T) {
	b, store, project, sessions := initialRunFixture(t, generatedInitialAgent)
	loop := createInitialLoop(t, b, project)
	b.loopService.Wait()
	// A new service recovers generation jobs before the scheduler starts.
	b.loopService.Close()
	replacement, err := loops.NewService(t.Context(), b.loops, initialRunner{run: generatedInitialAgent}, "auto", func() {})
	require.NoError(t, err)
	defer replacement.Close()
	b.loopService = replacement
	require.NoError(t, b.processInitialRun(t.Context()))
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	_, err = store.db.Exec(`UPDATE loop_project_tasks SET status='reserved' WHERE id=?`, detail.InitialRun.TaskID)
	require.NoError(t, err)
	require.NoError(t, b.processInitialRun(t.Context()))
	assert.EqualValues(t, 1, sessions.calls.Load())
}

func TestDeletingLoopPreventsInitialRun(t *testing.T) {
	b, store, project, sessions := initialRunFixture(t, generatedInitialAgent)
	loop := createInitialLoop(t, b, project)
	b.loopService.Wait()
	require.NoError(t, store.beginLoopDelete(t.Context(), loop.ID))
	require.NoError(t, b.processInitialRun(t.Context()))
	assert.Zero(t, sessions.calls.Load())
	rec := deleteLoopRequest(t, b, loop.ID)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	var n int
	require.NoError(t, store.db.Get(&n, `SELECT count(*) FROM loop_initial_runs WHERE loop_id=?`, loop.ID))
	assert.Zero(t, n)
}

func TestInitialRunReservationRollbackDoesNotConsumeIntent(t *testing.T) {
	b, store, project, sessions := initialRunFixture(t, generatedInitialAgent)
	loop := createInitialLoop(t, b, project)
	b.loopService.Wait()
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	_, err = store.db.Exec(`CREATE TRIGGER reject_initial BEFORE INSERT ON loop_project_tasks BEGIN SELECT RAISE(FAIL,'disk failure'); END`)
	require.NoError(t, err)
	b.taskLaunch.Lock()
	_, _, err = b.runLoopProjectTaskLocked(t.Context(), loop.ID, loopTaskRequest{RequestID: detail.InitialRun.TaskID, VersionID: detail.Versions[0].ID, ProjectID: project.ID, RepoPath: project.RepoPath, Agent: project.Agent})
	b.taskLaunch.Unlock()
	require.ErrorContains(t, err, "disk failure")
	detail, err = b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Equal(t, "waiting", detail.InitialRun.State)
	assert.Empty(t, detail.Loop.ActiveVersion)
	assert.Zero(t, sessions.calls.Load())
	cards, err := store.ListCards()
	require.NoError(t, err)
	assert.Empty(t, cards)
	_, err = store.db.Exec(`DROP TRIGGER reject_initial`)
	require.NoError(t, err)
	require.NoError(t, b.processInitialRun(t.Context()))
	assert.EqualValues(t, 1, sessions.calls.Load())
}

func TestPersistedUnlaunchedReservationIsNeverReplayed(t *testing.T) {
	b, store, project, sessions := initialRunFixture(t, generatedInitialAgent)
	loop := createInitialLoop(t, b, project)
	b.loopService.Wait()
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	task := &ProjectTask{ID: detail.InitialRun.TaskID, LoopID: loop.ID, ProjectID: project.ID, RepoPath: project.RepoPath, VersionID: detail.Versions[0].ID, CardID: "reserved-card", Status: "reserved"}
	require.NoError(t, store.ReserveProjectTask(task, &Card{ID: task.CardID, Status: StatusStarting}))
	require.NoError(t, b.processInitialRun(t.Context()))
	assert.Zero(t, sessions.calls.Load())
	detail, err = b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Equal(t, "admitted", detail.InitialRun.State)
}

func TestExistingLoopNeverGainsAutomaticInitialRun(t *testing.T) {
	b, _, project, sessions := initialRunFixture(t, generatedInitialAgent)
	loop, err := b.loops.Create(t.Context(), loops.Loop{Name: "Existing", Description: "Old task", Criteria: "Good", ProjectID: project.ID, Mode: loops.ModeProject})
	require.NoError(t, err)
	_, err = b.loopService.Generate(t.Context(), loop.ID)
	require.NoError(t, err)
	b.loopService.Wait()
	require.NoError(t, b.processInitialRun(t.Context()))
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Nil(t, detail.InitialRun)
	assert.Zero(t, sessions.calls.Load())
}

func TestCanceledGenerationDoesNotLaunchInitialRun(t *testing.T) {
	started := make(chan struct{})
	b, _, project, sessions := initialRunFixture(t, func(ctx context.Context, _ loops.Request) (loops.Result, error) {
		close(started)
		<-ctx.Done()
		return loops.Result{}, ctx.Err()
	})
	loop := createInitialLoop(t, b, project)
	<-started
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	b.loopService.Cancel(detail.InitialRun.JobID)
	b.loopService.Wait()
	require.NoError(t, b.processInitialRun(t.Context()))
	detail, err = b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", detail.InitialRun.State)
	assert.Contains(t, detail.InitialRun.Error, "canceled")
	assert.Zero(t, sessions.calls.Load())
}

func TestInitialRunRejectedAfterModeOrVersionChange(t *testing.T) {
	for _, change := range []string{"mode", "version"} {
		t.Run(change, func(t *testing.T) {
			b, _, project, sessions := initialRunFixture(t, generatedInitialAgent)
			loop := createInitialLoop(t, b, project)
			b.loopService.Wait()
			detail, err := b.loops.Get(t.Context(), loop.ID)
			require.NoError(t, err)
			if change == "mode" {
				require.NoError(t, b.loops.SetMode(t.Context(), loop.ID, loops.ModeAnalysis))
			} else {
				_, err := b.loopService.Generate(t.Context(), loop.ID)
				require.NoError(t, err)
				b.loopService.Wait()
				latest, err := b.loops.Get(t.Context(), loop.ID)
				require.NoError(t, err)
				require.NoError(t, b.loops.ActivateProjectVersion(t.Context(), loop.ID, latest.Versions[0].ID, detail.Loop.ActiveVersion))
			}
			require.NoError(t, b.processInitialRun(t.Context()))
			detail, err = b.loops.Get(t.Context(), loop.ID)
			require.NoError(t, err)
			assert.Equal(t, "failed", detail.InitialRun.State)
			assert.Zero(t, sessions.calls.Load())
		})
	}
}

func TestInitialRunPinsRelativeRepositoryAcrossRestart(t *testing.T) {
	b, store, project, sessions := initialRunFixture(t, generatedInitialAgent)
	parent := filepath.Dir(project.RepoPath)
	t.Chdir(parent)
	_, err := store.db.Exec(`UPDATE projects SET repo_path=? WHERE id=?`, filepath.Base(project.RepoPath), project.ID)
	require.NoError(t, err)
	loop := createInitialLoop(t, b, project)
	b.loopService.Wait()
	t.Chdir(t.TempDir())
	require.NoError(t, b.processInitialRun(t.Context()))
	tasks, err := store.ListProjectTasks(loop.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, project.RepoPath, tasks[0].RepoPath)
	assert.EqualValues(t, 1, sessions.calls.Load())
}

func TestInitialWorkerStopsWhileWaitingForLaunchLock(t *testing.T) {
	b, _ := newTestBoard(t)
	b.taskLaunch.Lock()
	defer b.taskLaunch.Unlock()
	stop := b.startInitialRuns(t.Context())
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow(t, "initial scheduler did not stop")
	}
}

type cancellableInitialSessions struct {
	noopSessionManager

	started chan struct{}
}

func (s *cancellableInitialSessions) NewSessionContext(ctx context.Context, _, _, _, _, _, _, _, _ string) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestInitialWorkerStopsDuringLaunchWithoutReplaying(t *testing.T) {
	b, store, project, _ := initialRunFixture(t, generatedInitialAgent)
	loop := createInitialLoop(t, b, project)
	b.loopService.Wait()
	sessions := &cancellableInitialSessions{started: make(chan struct{})}
	b.sessions = sessions
	stop := b.startInitialRuns(t.Context())
	<-sessions.started
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow(t, "launch did not stop")
	}
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Equal(t, "admitted", detail.InitialRun.State)
	task, err := store.GetProjectTask(detail.InitialRun.TaskID)
	require.NoError(t, err)
	assert.Equal(t, "launch_uncertain", task.Status)
	require.NoError(t, b.processInitialRun(t.Context()))
}

func TestInitialLaunchPersistenceFailureIsVisibleAndNotReplayed(t *testing.T) {
	b, store, project, sessions := initialRunFixture(t, generatedInitialAgent)
	loop := createInitialLoop(t, b, project)
	b.loopService.Wait()
	_, err := store.db.Exec(`CREATE TRIGGER fail_initial_status BEFORE UPDATE ON loop_project_tasks BEGIN SELECT RAISE(FAIL,'status failure'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, b.processInitialRun(t.Context()), "status failure")
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Equal(t, "admitted", detail.InitialRun.State)
	assert.Contains(t, detail.InitialRun.Error, "status failure")
	require.NoError(t, b.processInitialRun(t.Context()))
	assert.EqualValues(t, 1, sessions.calls.Load())
}

func TestAmbiguousInitialLaunchPersistenceFailureIsVisible(t *testing.T) {
	b, store, project, sessions := initialRunFixture(t, generatedInitialAgent)
	sessions.launch = func(string, string) error { return errors.New("delivery uncertain") }
	loop := createInitialLoop(t, b, project)
	b.loopService.Wait()
	_, err := store.db.Exec(`CREATE TRIGGER fail_initial_status BEFORE UPDATE ON loop_project_tasks BEGIN SELECT RAISE(FAIL,'status failure'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, b.processInitialRun(t.Context()), "delivery uncertain")
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Equal(t, "admitted", detail.InitialRun.State)
	assert.Contains(t, detail.InitialRun.Error, "status failure")
	require.NoError(t, b.processInitialRun(t.Context()))
	assert.EqualValues(t, 1, sessions.calls.Load())
}

func TestInitialWorkerShutdownBoundedAfterLaunchDelivery(t *testing.T) {
	b, store, project, sessions := initialRunFixture(t, generatedInitialAgent)
	_ = createInitialLoop(t, b, project)
	b.loopService.Wait()
	held := make(chan *sql.Conn, 1)
	sessions.launch = func(string, string) error {
		conn, err := store.db.Conn(t.Context())
		if err != nil {
			return err
		}
		held <- conn
		return nil
	}
	stop := b.startInitialRuns(t.Context())
	conn := <-held
	defer conn.Close()
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		require.FailNow(t, "post-launch DB work blocked shutdown")
	}
}

func TestInitialLaunchObservationSurvivesPersistenceTimeout(t *testing.T) {
	b, store, project, sessions := initialRunFixture(t, generatedInitialAgent)
	loop := createInitialLoop(t, b, project)
	b.loopService.Wait()
	held := make(chan *sql.Conn, 1)
	sessions.launch = func(string, string) error {
		conn, err := store.db.Conn(t.Context())
		if err != nil {
			return err
		}
		held <- conn
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- b.processInitialRun(t.Context()) }()
	conn := <-held
	require.Eventually(t, func() bool {
		b.controller.mu.Lock()
		defer b.controller.mu.Unlock()
		return len(b.controller.watchers) > 0
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, conn.Close())
	require.Error(t, <-done)
	detail, err := b.loops.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Equal(t, "admitted", detail.InitialRun.State)
	require.NoError(t, b.processInitialRun(t.Context()))
	assert.EqualValues(t, 1, sessions.calls.Load())
}
