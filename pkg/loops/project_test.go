package loops

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testProject(t *testing.T, store *Store) ProjectSnapshot {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=0", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("init")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("A committed project"), 0o600))
	git("add", ".")
	git("-c", "commit.gpgsign=false", "commit", "-m", "initial")
	_, err := store.db.Exec(`CREATE TABLE IF NOT EXISTS projects (id TEXT PRIMARY KEY,name TEXT NOT NULL,repo_path TEXT NOT NULL, agent TEXT NOT NULL DEFAULT 'test-agent')`)
	require.NoError(t, err)
	project := ProjectSnapshot{ID: newID(), Name: "Example project", Path: dir}
	_, err = store.db.Exec(`INSERT INTO projects (id,name,repo_path) VALUES (?,?,?)`, project.ID, project.Name, project.Path)
	require.NoError(t, err)
	return project
}

func TestProjectSnapshotUsesPinnedCommittedFiles(t *testing.T) {
	store := testStore(t)
	project := testProject(t, store)
	snapshot, err := store.projectSnapshot(t.Context(), project.ID)
	require.NoError(t, err)
	require.Len(t, snapshot.Files, 1)
	assert.Equal(t, "A committed project", snapshot.Files[0].Content)
	assert.Len(t, snapshot.Revision, 40)
	assert.Len(t, snapshot.Digest, 64)
	require.NoError(t, os.WriteFile(filepath.Join(project.Path, "README.md"), []byte("Uncommitted secret"), 0o600))
	next, err := store.projectSnapshot(t.Context(), project.ID)
	require.NoError(t, err)
	assert.Equal(t, snapshot, next)
}

func TestRunsRequireExistingProject(t *testing.T) {
	store := testStore(t)
	project := testProject(t, store)
	loop := testLoop(t, store)
	version, err := store.AddVersion(t.Context(), loop.ID, "agents:\n  root:\n    model: auto\n    instruction: Describe project\n", "")
	require.NoError(t, err)
	service, err := NewService(t.Context(), store, fakeRunner{run: func(_ context.Context, request Request, _ func(Result)) (Result, error) {
		assert.Contains(t, request.Input, "A committed project")
		assert.Contains(t, request.Input, loop.Description)
		return Result{Output: "result"}, nil
	}}, "auto", func() {})
	require.NoError(t, err)
	_, err = service.Run(t.Context(), loop.ID, version.ID, "", "")
	require.ErrorIs(t, err, ErrInvalid)
	_, err = service.Run(t.Context(), loop.ID, version.ID, "", "missing")
	require.ErrorIs(t, err, ErrNotFound)
	job, err := service.Run(t.Context(), loop.ID, version.ID, "", project.ID)
	require.NoError(t, err)
	service.Wait()
	saved, err := store.Job(t.Context(), loop.ID, job.ID)
	require.NoError(t, err)
	require.NotNil(t, saved.Project)
	assert.Equal(t, project.ID, saved.Project.ID)
	assert.Equal(t, "succeeded", saved.Status)
	_, err = store.db.Exec(`DELETE FROM projects WHERE id=?`, project.ID)
	require.NoError(t, err)
	saved, err = store.Job(t.Context(), loop.ID, job.ID)
	require.NoError(t, err)
	assert.Equal(t, project.Name, saved.Project.Name)
}

func TestProjectContextSkipsSecretAndNonregularFiles(t *testing.T) {
	for _, name := range []string{".env", ".git/config", "secrets.go", "private/credentials.txt", "vendor/a.go", "node_modules/a.js", "a.png", "key.pem"} {
		assert.False(t, projectTextFile(name), name)
	}
	for _, name := range []string{"README.md", "src/main.go", "package.json", "go.mod"} {
		assert.True(t, projectTextFile(name), name)
	}
}

func TestSnapshotIgnoresSymlinksAndBoundsContent(t *testing.T) {
	store := testStore(t)
	project := testProject(t, store)
	require.NoError(t, os.WriteFile(filepath.Join(project.Path, "secrets.txt"), []byte("secret-not-for-model"), 0o600))
	require.NoError(t, os.Symlink("secrets.txt", filepath.Join(project.Path, "linked.txt")))
	require.NoError(t, os.WriteFile(filepath.Join(project.Path, "large.md"), []byte(strings.Repeat("x", 20000)), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(project.Path, "binary.txt"), []byte{0, 1, 2}, 0o600))
	for _, args := range [][]string{{"add", "."}, {"-c", "commit.gpgsign=false", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "fixtures"}} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = project.Path
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))
	}
	snapshot, err := store.projectSnapshot(t.Context(), project.ID)
	require.NoError(t, err)
	require.Len(t, snapshot.Files, 1)
	assert.Equal(t, 4, snapshot.Omitted)
	data, err := json.Marshal(snapshot)
	require.NoError(t, err)
	assert.Less(t, len(data), maxProjectContext)
	assert.NotContains(t, string(data), "secret-not-for-model")
}

func TestSnapshotIgnoresAmbientGitDirectory(t *testing.T) {
	store := testStore(t)
	project := testProject(t, store)
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "missing"))
	snapshot, err := store.projectSnapshot(t.Context(), project.ID)
	require.NoError(t, err)
	assert.Equal(t, "README.md", snapshot.Files[0].Path)
}

func TestSnapshotRejectsMissingRepository(t *testing.T) {
	store := testStore(t)
	project := testProject(t, store)
	require.NoError(t, os.RemoveAll(filepath.Join(project.Path, ".git")))
	_, err := store.projectSnapshot(t.Context(), project.ID)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestProjectMigrationPreservesLegacyData(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	job, err := store.newJob(t.Context(), loop.ID, "v1", "run", "legacy input")
	require.NoError(t, err)
	_, err = store.db.Exec(`ALTER TABLE loops DROP COLUMN project_id; ALTER TABLE loop_jobs DROP COLUMN project;`)
	require.NoError(t, err)
	_, err = NewStore(t.Context(), store.db)
	require.NoError(t, err)
	_, err = NewStore(t.Context(), store.db)
	require.NoError(t, err)
	detail, err := store.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Equal(t, loop.Example, detail.Loop.Example)
	assert.Empty(t, detail.Loop.ProjectID)
	old, err := store.Job(t.Context(), loop.ID, job.ID)
	require.NoError(t, err)
	assert.Nil(t, old.Project)
	assert.Equal(t, "legacy input", old.Input)
}

func TestSnapshotRejectsSubdirectoryScope(t *testing.T) {
	store := testStore(t)
	project := testProject(t, store)
	nested := filepath.Join(project.Path, "nested")
	require.NoError(t, os.Mkdir(nested, 0o700))
	_, err := store.db.Exec(`UPDATE projects SET repo_path=? WHERE id=?`, nested, project.ID)
	require.NoError(t, err)
	_, err = store.projectSnapshot(t.Context(), project.ID)
	require.ErrorIs(t, err, ErrInvalid)
	assert.Contains(t, err.Error(), "repository root")
}

func TestSnapshotResolvesExistingRelativeProject(t *testing.T) {
	store := testStore(t)
	project := testProject(t, store)
	t.Chdir(filepath.Dir(project.Path))
	_, err := store.db.Exec(`UPDATE projects SET repo_path=? WHERE id=?`, filepath.Base(project.Path), project.ID)
	require.NoError(t, err)
	snapshot, err := store.projectSnapshot(t.Context(), project.ID)
	require.NoError(t, err)
	assert.Equal(t, project.Path, snapshot.Path)
}

func TestSnapshotChangesOnlyAfterNewCommit(t *testing.T) {
	store := testStore(t)
	project := testProject(t, store)
	before, err := store.projectSnapshot(t.Context(), project.ID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(project.Path, "README.md"), []byte("A new committed project"), 0o600))
	for _, args := range [][]string{{"add", "."}, {"-c", "commit.gpgsign=false", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "change"}} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = project.Path
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	after, err := store.projectSnapshot(t.Context(), project.ID)
	require.NoError(t, err)
	assert.NotEqual(t, before.Revision, after.Revision)
	assert.NotEqual(t, before.Digest, after.Digest)
	assert.Equal(t, "A committed project", before.Files[0].Content)
	assert.Equal(t, "A new committed project", after.Files[0].Content)
}
