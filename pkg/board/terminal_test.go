package board

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTerminalDim(t *testing.T) {
	assert.Equal(t, uint16(120), terminalDim("120", 80))
	assert.Equal(t, uint16(80), terminalDim("", 80))
	assert.Equal(t, uint16(80), terminalDim("abc", 80))
	assert.Equal(t, uint16(80), terminalDim("0", 80))
	assert.Equal(t, uint16(80), terminalDim("-1", 80))
	assert.Equal(t, uint16(80), terminalDim("70000", 80))
}
