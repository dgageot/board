package loops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoopHTTP(t *testing.T) {
	store := testStore(t)
	project := testProject(t, store)
	mux := http.NewServeMux()
	service, err := NewService(t.Context(), store, fakeRunner{run: func(context.Context, Request, func(Result)) (Result, error) {
		return Result{Output: `{"instruction":"Summarize","model":"auto","rationale":"First"}`}, nil
	}}, "auto", func() {})
	require.NoError(t, err)
	changes := 0
	Register(mux, store, func() { changes++ }, service)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	rec := request("POST", "/api/loops", `{"name":"Notes","description":"Summarize","criteria":"Accurate","projectId":"`+project.ID+`"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var loop Loop
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &loop))
	rec = request("POST", "/api/loops/"+loop.ID+"/versions", `{"yaml":"agents:\n  root:\n    model: auto\n","rationale":"First"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var version Version
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &version))
	assert.Equal(t, 1, changes)
	service.Wait()
	rec = request("GET", "/api/loops/"+loop.ID+"/versions/"+version.ID+"/export", "")
	assert.Equal(t, version.YAML, rec.Body.String())
	assert.Contains(t, rec.Header().Get("Content-Disposition"), "agent.yaml")
	assert.Equal(t, http.StatusNotFound, request("GET", "/api/loops/other/versions/"+version.ID+"/export", "").Code)
	assert.Equal(t, http.StatusBadRequest, request("POST", "/api/loops", `{} {}`).Code)
	assert.Equal(t, http.StatusBadRequest, request("POST", "/api/loops", `{"unknown":true}`).Code)
}

func TestCreateLoopRequiresFirstProject(t *testing.T) {
	store := testStore(t)
	_ = testProject(t, store)
	mux := http.NewServeMux()
	service, err := NewService(t.Context(), store, fakeRunner{}, "auto", func() {})
	require.NoError(t, err)
	Register(mux, store, func() {}, service)
	for _, body := range []string{`{"name":"Task","description":"Work","criteria":"Good"}`, `{"name":"Task","description":"Work","criteria":"Good","projectId":"missing"}`} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/loops", strings.NewReader(body)))
		assert.Contains(t, []int{http.StatusBadRequest, http.StatusNotFound}, rec.Code)
	}
	all, err := store.List(t.Context())
	require.NoError(t, err)
	assert.Empty(t, all)
}

func TestProjectInstructionImportIsRunnableButRejectsTools(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	require.NoError(t, store.SetMode(t.Context(), loop.ID, ModeProject))
	mux := http.NewServeMux()
	Register(mux, store, func() {})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/loops/"+loop.ID+"/versions", strings.NewReader(`{"project":true,"yaml":"agents:\n  root:\n    model: auto\n    instruction: Implement and verify the task.\n"}`)))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var version Version
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &version))
	assert.Equal(t, ModeProject, version.Mode)
	_, err := ProjectInstruction(version)
	require.NoError(t, err)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/loops/"+loop.ID+"/versions", strings.NewReader(`{"project":true,"yaml":"agents:\n  root:\n    model: auto\n    instruction: Work\n    toolsets: [{type: shell}]\n"}`)))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
