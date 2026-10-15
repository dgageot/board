package board

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSharedCredentialsOptIn(t *testing.T) {
	// t.Setenv registers restoration of the original value, including absence.
	t.Setenv("BOARD_CREDENTIALS", "")
	require.NoError(t, os.Unsetenv("BOARD_CREDENTIALS"))
	assert.False(t, DefaultConfig().SharedCredentials, "disabled when unset")

	for _, value := range []string{"", "0", "off", "false", "true", "on", " 1 ", "1"} {
		t.Run("value="+value, func(t *testing.T) {
			t.Setenv("BOARD_CREDENTIALS", value)
			assert.Equal(t, value == "1", DefaultConfig().SharedCredentials)
		})
	}
}
