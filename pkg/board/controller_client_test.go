package board

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type cleanupClient struct {
	fakeClient

	closed int
}

func (c *cleanupClient) CloseIdleConnections() { c.closed++ }

func TestControllerClosesOneShotClients(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			client := &cleanupClient{}
			if fail {
				err := errors.New("unavailable")
				client.snapErr, client.transcriptErr, client.followErr = err, err, err
			}
			c := newTestController(t, openTestStore(t), newFakeSessionManager(), client)
			require.Equal(t, !fail, c.Ready(devCard()))
			require.Equal(t, 1, client.closed)
			_, err := c.Transcript(t.Context(), devCard())
			require.Equal(t, fail, err != nil)
			require.Equal(t, 2, client.closed)
			err = c.SendPrompt(devCard(), "hi")
			require.Equal(t, fail, err != nil)
			require.Equal(t, 3, client.closed)
			require.NoError(t, c.SendPrompt(devCard(), ""))
			require.Equal(t, 3, client.closed, "empty prompts allocate no client")
		})
	}
}
