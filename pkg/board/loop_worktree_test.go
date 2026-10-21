package board

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dgageot/board/pkg/git"
)

type contextWorktreeSessions struct {
	*fakeSessionManager

	contextCalls int
}

func (s *contextWorktreeSessions) NewSessionContext(ctx context.Context, name, workDir, agent, sessionID, listenSocket, worktreeName, worktreeBase, prompt string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.contextCalls++
	return s.NewSession(name, workDir, agent, sessionID, listenSocket, worktreeName, worktreeBase, prompt)
}

func TestLoopRunsUseDistinctWorktrees(t *testing.T) {
	for _, contextLaunch := range []bool{false, true} {
		name := "NewSession"
		if contextLaunch {
			name = "NewSessionContext"
		}
		t.Run(name, func(t *testing.T) {
			b, store, project, _ := initialRunFixture(t, generatedInitialAgent)
			sessions := newFakeSessionManager()
			contextSessions := &contextWorktreeSessions{fakeSessionManager: sessions}
			b.sessions = sessions
			if contextLaunch {
				b.sessions = contextSessions
			}

			loop := createInitialLoop(t, b, project)
			b.loopService.Wait()
			require.NoError(t, b.processInitialRun(t.Context()))
			detail, err := b.loops.Get(t.Context(), loop.ID)
			require.NoError(t, err)
			require.NotNil(t, detail.InitialRun)
			first, err := store.GetProjectTask(detail.InitialRun.TaskID)
			require.NoError(t, err)

			body, err := json.Marshal(loopTaskRequest{RequestID: "second-run", VersionID: first.VersionID, ProjectID: project.ID, RepoPath: project.RepoPath, Agent: project.Agent})
			require.NoError(t, err)
			request := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/api/loops/"+loop.ID+"/tasks", strings.NewReader(string(body)))
				req.SetPathValue("id", loop.ID)
				rec := httptest.NewRecorder()
				b.handleRunLoopProjectTask(rec, req)
				return rec
			}
			rec := request()
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			second, err := store.GetProjectTask("second-run")
			require.NoError(t, err)

			require.Len(t, sessions.created, 2)
			base, err := git.ExecutionBase(t.Context(), project.RepoPath)
			require.NoError(t, err)
			for i, task := range []*ProjectTask{first, second} {
				card, err := store.GetCard(task.CardID)
				require.NoError(t, err)
				call := sessions.created[i]
				require.NotEmpty(t, call.worktreeName)
				assert.Equal(t, project.RepoPath, call.workDir)
				assert.Equal(t, base, call.worktreeBase)
				assert.Equal(t, task.Base, call.worktreeBase)
				assert.Equal(t, git.WorktreeDir(call.worktreeName), card.Worktree)
				assert.Equal(t, git.WorktreeBranch(call.worktreeName), card.Branch)
				assert.Equal(t, call.worktreeName, filepath.Base(card.Worktree))
				assert.NotEqual(t, project.RepoPath, card.Worktree)
				assert.Equal(t, card.Session, call.name)
				assert.Equal(t, card.AgentSession, call.sessionID)
			}
			assert.NotEqual(t, sessions.created[0].worktreeName, sessions.created[1].worktreeName)
			assert.NotEqual(t, sessions.created[0].name, sessions.created[1].name)
			assert.NotEqual(t, sessions.created[0].sessionID, sessions.created[1].sessionID)

			rec = request()
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.NoError(t, b.processInitialRun(t.Context()))
			assert.Len(t, sessions.created, 2, "retries must not launch extra worktrees")
			if contextLaunch {
				assert.Equal(t, 2, contextSessions.contextCalls)
			}
		})
	}
}
