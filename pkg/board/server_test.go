package board

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()

	b, _ := newTestBoard(t)
	handler, err := buildMux(b)
	require.NoError(t, err)
	return handler
}

func TestStaticUIHasSoundNotificationsDisabledByDefault(t *testing.T) {
	handler := testHandler(t)

	req := httptest.NewRequest(http.MethodGet, "http://localhost:8077/", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	// The icon is rendered by app.js from the persisted preference; the
	// markup only carries the default "off" state.
	assert.Contains(t, rec.Body.String(), `id="btn-sound"`)
	assert.Contains(t, rec.Body.String(), `aria-pressed="false"`)
	assert.Contains(t, rec.Body.String(), `id="i-bell-off"`)
	assert.NotContains(t, rec.Body.String(), `🔔`)
}

func TestWriteJSONStatusSetsContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONStatus(rec, http.StatusCreated, map[string]string{"a": "b"})

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"a":"b"}`, rec.Body.String())
}

func TestCSRFProtectRejectsCrossOriginWrites(t *testing.T) {
	handler := testHandler(t)

	req := httptest.NewRequest(http.MethodPost, "http://localhost:8077/api/cards", strings.NewReader(`{}`))
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestCSRFProtectAllowsSameOriginWrites(t *testing.T) {
	handler := testHandler(t)

	req := httptest.NewRequest(http.MethodPost, "http://localhost:8077/api/cards", strings.NewReader(`{}`))
	req.Header.Set("Origin", "http://localhost:8077")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Passes the CSRF check and reaches the handler (which rejects the empty prompt).
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCSRFProtectAllowsNonBrowserWrites(t *testing.T) {
	handler := testHandler(t)

	req := httptest.NewRequest(http.MethodPost, "http://localhost:8077/api/cards", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCSRFProtectAllowsCrossOriginReads(t *testing.T) {
	handler := testHandler(t)

	req := httptest.NewRequest(http.MethodGet, "http://localhost:8077/api/cards", http.NoBody)
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestLoopCreationDoesNotAskForExample(t *testing.T) {
	handler := testHandler(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), `id="loop-example"`)
	assert.NotContains(t, rec.Body.String(), "An example input")
}

func TestNewLoopFieldsDisablePasswordCompletion(t *testing.T) {
	handler := testHandler(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	require.Equal(t, http.StatusOK, rec.Code)
	_, dialog, found := strings.Cut(rec.Body.String(), `<dialog id="loop-new-dialog"`)
	require.True(t, found)
	dialog, _, found = strings.Cut(dialog, "</dialog>")
	require.True(t, found)
	fields := regexp.MustCompile(`<(?:input|select|textarea)\b[^>]*>`).FindAllString(dialog, -1)
	require.Len(t, fields, 5)
	for _, field := range fields {
		assert.Contains(t, field, `autocomplete="off"`)
		assert.Contains(t, field, `data-1p-ignore`)
	}
}

func TestLoopsCopyKeepsOperationalGuidanceWithoutSlogans(t *testing.T) {
	handler := testHandler(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	require.Equal(t, http.StatusOK, rec.Code)
	page := rec.Body.String()
	assert.NotContains(t, page, "Great agents aren't a lucky prompt")
	assert.NotContains(t, page, "BUILD. MEASURE. EVOLVE.")
	assert.NotContains(t, page, "Two new agents. One baseline.")
	assert.Contains(t, page, "Creates an agent immediately. Uses provider credits.")
	assert.Contains(t, page, "Sends selected committed files to your model provider")
	assert.Contains(t, page, "Promotion is manual.")
}

func TestProjectTaskConsentExplainsExecutionPrivileges(t *testing.T) {
	handler := testHandler(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	require.Equal(t, http.StatusOK, rec.Code)
	_, dialog, found := strings.Cut(rec.Body.String(), `<dialog id="loop-project-task-dialog"`)
	require.True(t, found)
	dialog, _, found = strings.Cut(dialog, "</dialog>")
	require.True(t, found)
	assert.Contains(t, dialog, `id="loop-task-consent" type="checkbox" required`)
	assert.Contains(t, dialog, "automatic tool approval")
	assert.Contains(t, dialog, "not a security sandbox")
	assert.Contains(t, dialog, "publish remotely")
	assert.Contains(t, dialog, "selected workflow instruction")
}
