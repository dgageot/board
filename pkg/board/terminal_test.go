package board

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSameOrigin(t *testing.T) {
	req := func(origin string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://localhost:8077/api/terminal/s1", http.NoBody)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}

	assert.True(t, sameOrigin(req("")), "non-browser clients send no Origin")
	assert.True(t, sameOrigin(req("http://localhost:8077")))
	assert.True(t, sameOrigin(req("http://LOCALHOST:8077")), "host comparison is case-insensitive")
	assert.False(t, sameOrigin(req("http://evil.example")))
	assert.False(t, sameOrigin(req("http://localhost:9999")), "different port is a different origin")
	assert.False(t, sameOrigin(req("::bad::origin::")))
}

func TestTerminalDim(t *testing.T) {
	assert.Equal(t, uint16(120), terminalDim("120", 80))
	assert.Equal(t, uint16(80), terminalDim("", 80))
	assert.Equal(t, uint16(80), terminalDim("abc", 80))
	assert.Equal(t, uint16(80), terminalDim("0", 80))
	assert.Equal(t, uint16(80), terminalDim("-1", 80))
	assert.Equal(t, uint16(80), terminalDim("70000", 80))
}
