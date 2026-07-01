package board

import (
	"net/http"
	"net/http/httptest"
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
