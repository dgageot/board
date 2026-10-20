package loops

import (
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
	mux := http.NewServeMux()
	changes := 0
	Register(mux, store, func() { changes++ })
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	rec := request("POST", "/api/loops", `{"name":"Notes","description":"Summarize","criteria":"Accurate","example":"fix"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var loop Loop
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &loop))
	rec = request("POST", "/api/loops/"+loop.ID+"/versions", `{"yaml":"agents:\n  root:\n    model: auto\n","rationale":"First"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var version Version
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &version))
	assert.Equal(t, 2, changes)
	rec = request("GET", "/api/loops/"+loop.ID+"/versions/"+version.ID+"/export", "")
	assert.Equal(t, version.YAML, rec.Body.String())
	assert.Contains(t, rec.Header().Get("Content-Disposition"), "agent.yaml")
	assert.Equal(t, http.StatusNotFound, request("GET", "/api/loops/other/versions/"+version.ID+"/export", "").Code)
	assert.Equal(t, http.StatusBadRequest, request("POST", "/api/loops", `{} {}`).Code)
	assert.Equal(t, http.StatusBadRequest, request("POST", "/api/loops", `{"unknown":true}`).Code)
}
