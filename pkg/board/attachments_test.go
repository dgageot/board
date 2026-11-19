package board

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func isolateAttachmentDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("AppData", dir)
		return
	}
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
}

type testUploadFile struct {
	name    string
	content io.Reader
}

func cardUpload(t *testing.T, prompt string, files ...testUploadFile) *http.Request {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	task, err := json.Marshal(createCardRequest{Prompt: prompt, ProjectID: "project"})
	require.NoError(t, err)
	require.NoError(t, form.WriteField("task", string(task)))
	for _, file := range files {
		part, err := form.CreateFormFile("attachments", file.name)
		require.NoError(t, err)
		_, err = io.Copy(part, file.content)
		require.NoError(t, err)
	}
	require.NoError(t, form.Close())
	request := httptest.NewRequest(http.MethodPost, "/api/cards", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	return request
}

func TestCreateCardWithAttachments(t *testing.T) {
	isolateAttachmentDir(t)
	b, store := newTestBoard(t)
	require.NoError(t, store.SeedColumns(defaultColumns))
	sessions := newFakeSessionManager()
	b.sessions = sessions
	b.controller.sessions = sessions
	repo := projectTestRepo(t)
	require.NoError(t, store.InsertProject(&Project{ID: "project", Name: "Project", RepoPath: repo, Agent: "/agent"}))

	r := cardUpload(t, "Inspect these files\nThen summarize",
		testUploadFile{`..\notes.txt`, strings.NewReader("first")},
		testUploadFile{"notes.txt", strings.NewReader("second")},
		testUploadFile{"screen shot.png", bytes.NewReader([]byte{0, 1, 255})},
	)
	w := httptest.NewRecorder()
	b.handleCreateCard(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var card Card
	require.NoError(t, json.NewDecoder(w.Body).Decode(&card))
	t.Cleanup(func() { b.controller.Stop(card.ID) })
	assert.Equal(t, "Inspect these files\nThen summarize", card.Prompt)
	assert.Equal(t, "Inspect these files", card.Title)
	stored, err := store.GetCard(card.ID)
	require.NoError(t, err)
	assert.Equal(t, card.Prompt, stored.Prompt)

	dir, err := cardAttachmentDir(card.ID)
	require.NoError(t, err)
	require.DirExists(t, dir)
	assert.False(t, strings.HasPrefix(dir, repo+string(filepath.Separator)))
	require.Len(t, sessions.calls(), 1)
	prompt := sessions.calls()[0].prompt
	assert.True(t, strings.HasPrefix(prompt, card.Prompt+"\n\n"))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	for i, data := range []string{"first", "second", "\x00\x01\xff"} {
		path := filepath.Join(dir, entries[i].Name())
		assert.Contains(t, prompt, fmt.Sprintf("%q", path))
		assert.NoFileExists(t, filepath.Join(card.Worktree, entries[i].Name()))
		contents, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, data, string(contents))
		if runtime.GOOS != "windows" {
			info, err := entries[i].Info()
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		}
	}

	require.NoError(t, b.deleteCard(&card))
	assert.NoDirExists(t, dir)
}

type failingAttachmentLauncher struct{ noopSessionManager }

func (failingAttachmentLauncher) NewSession(string, string, string, string, string, string, string, string, string) error {
	return errors.New("launch failed")
}

func TestFailedCardLaunchRemovesAttachments(t *testing.T) {
	isolateAttachmentDir(t)
	b, store := newTestBoard(t)
	require.NoError(t, store.SeedColumns(defaultColumns))
	require.NoError(t, store.InsertProject(&Project{ID: "project", Name: "Project", RepoPath: projectTestRepo(t)}))
	b.sessions = failingAttachmentLauncher{}

	w := httptest.NewRecorder()
	b.handleCreateCard(w, cardUpload(t, "prompt", testUploadFile{"notes.txt", strings.NewReader("file contents")}))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	cards, err := store.ListCards()
	require.NoError(t, err)
	assert.Empty(t, cards)
	dir, err := cardAttachmentDir("unused")
	require.NoError(t, err)
	entries, err := os.ReadDir(filepath.Dir(dir))
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestCreateCardAttachmentLimits(t *testing.T) {
	isolateAttachmentDir(t)
	b, store := newTestBoard(t)
	for _, tc := range []struct {
		name     string
		prompt   string
		count    int
		fileSize int
		status   int
	}{
		{"missing prompt", " ", 1, 0, http.StatusBadRequest},
		{"too many files", "prompt", maxCardAttachments + 1, 0, http.StatusBadRequest},
		{"too much file content", "prompt", 2, maxCardAttachmentBytes/2 + 1, http.StatusRequestEntityTooLarge},
		{"oversized request", "prompt", 1, maxCardUploadBytes + 1, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Repeat("a", tc.fileSize)
			files := make([]testUploadFile, tc.count)
			for i := range files {
				files[i] = testUploadFile{"test", strings.NewReader(data)}
			}
			w := httptest.NewRecorder()
			b.handleCreateCard(w, cardUpload(t, tc.prompt, files...))
			assert.Equal(t, tc.status, w.Code, w.Body.String())
		})
	}
	cards, err := store.ListCards()
	require.NoError(t, err)
	assert.Empty(t, cards)
}
