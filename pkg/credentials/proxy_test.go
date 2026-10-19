package credentials

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func field(value []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(value)))
	b.Write(value)
	return b.Bytes()
}

func signingRequest(namespace string) []byte {
	data := append([]byte("SSHSIG"), field([]byte(namespace))...)
	data = append(data, field(nil)...)
	data = append(data, field([]byte("sha512"))...)
	data = append(data, field(make([]byte, 64))...)
	request := append([]byte{13}, field([]byte("public key"))...)
	request = append(request, field(data)...)
	return append(request, 0, 0, 0, 0)
}

func TestPermitted(t *testing.T) {
	assert.True(t, permitted([]byte{11}))
	assert.True(t, permitted(signingRequest("git")))
	for _, message := range [][]byte{
		nil,
		{11, 0},
		{17},
		{13},
		{13, 255, 255, 255, 255},
		signingRequest("ssh"), signingRequest("file"),
		append(signingRequest("git"), 0),
	} {
		assert.False(t, permitted(message), "%x", message)
	}
	valid := signingRequest("git")
	for i := range valid {
		assert.False(t, permitted(valid[:i]), "truncated at %d", i)
	}
}

func TestMessageFraming(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, writeMessage(&b, []byte{11}))
	message, err := readMessage(&b)
	require.NoError(t, err)
	assert.Equal(t, []byte{11}, message)
	for _, input := range [][]byte{nil, {0, 0}, {0, 0, 0, 0}, {255, 255, 255, 255}, {0, 0, 0, 2, 11}} {
		_, err := readMessage(bytes.NewReader(input))
		require.Error(t, err)
	}
}

func TestProxySharesConnectionAndFilters(t *testing.T) {
	dir := socketDir(t)
	listener, err := listenSocket(dir + "/upstream")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	var connections, requests atomic.Int32
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		connections.Add(1)
		defer func() { _ = conn.Close() }()
		for {
			_, err := readMessage(conn)
			if err != nil {
				return
			}
			requests.Add(1)
			if writeMessage(conn, []byte{12, 0, 0, 0, 0}) != nil {
				return
			}
		}
	}()
	s, err := Start(t.Context(), dir+"/helper", listener.Addr().String(), nil)
	require.NoError(t, err)
	defer s.Close()
	var wg sync.WaitGroup
	errors := make(chan error, 32)
	for range 8 {
		wg.Go(func() {
			conn, err := net.Dial("unix", dir+"/helper/agent.sock")
			if err != nil {
				errors <- err
				return
			}
			defer func() { _ = conn.Close() }()
			if err := writeMessage(conn, []byte{11}); err != nil {
				errors <- err
				return
			}
			response, err := readMessage(conn)
			errors <- err
			assert.Equal(t, []byte{12, 0, 0, 0, 0}, response)
			if err := writeMessage(conn, signingRequest("ssh")); err != nil {
				errors <- err
				return
			}
			response, err = readMessage(conn)
			errors <- err
			assert.Equal(t, []byte{5}, response)
		})
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, connections.Load())
	assert.EqualValues(t, 8, requests.Load())
}

func TestCloseInterruptsSigning(t *testing.T) {
	dir := socketDir(t)
	upstream, err := listenSocket(dir + "/upstream")
	require.NoError(t, err)
	defer func() { _ = upstream.Close() }()
	received := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, err := readMessage(conn); err != nil {
			return
		}
		close(received)
		_, _ = io.Copy(io.Discard, conn)
	}()
	s, err := Start(t.Context(), dir+"/helper", upstream.Addr().String(), nil)
	require.NoError(t, err)
	defer s.Close()
	conn, err := net.Dial("unix", dir+"/helper/agent.sock")
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, writeMessage(conn, signingRequest("git")))
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached upstream")
	}
	s.Close()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream connection survived helper shutdown")
	}
}

func TestProxyRecoversUpstreamConnection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request []byte
		reject  bool
		retry   bool
	}{
		{name: "retry identity listing", request: []byte{11}, retry: true},
		{name: "never replay signing", request: signingRequest("git")},
		{name: "reset rejected signing", request: signingRequest("git"), reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := socketDir(t)
			listener, err := listenSocket(dir + "/upstream")
			require.NoError(t, err)
			defer func() { _ = listener.Close() }()
			stale, upstream := net.Pipe()
			defer func() { _ = upstream.Close() }()
			proxy := &signingProxy{source: listener.Addr().String(), requests: make(chan struct{}, 1), upstream: stale}
			defer proxy.close()
			go func() {
				defer func() { _ = upstream.Close() }()
				_, _ = readMessage(upstream)
				if tc.reject {
					_ = writeMessage(upstream, []byte{5})
					// A rejection does not close the connection on the agent's side.
					_, _ = io.Copy(io.Discard, upstream)
				}
			}()
			freshRequest := make(chan []byte, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				request, err := readMessage(conn)
				if err == nil {
					freshRequest <- request
					_ = writeMessage(conn, []byte{12, 0, 0, 0, 0})
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			response, err := proxy.exchange(ctx, tc.request)
			switch {
			case tc.retry:
				require.NoError(t, err)
				assert.Equal(t, []byte{12, 0, 0, 0, 0}, response)
			case tc.reject:
				require.NoError(t, err)
				assert.Equal(t, []byte{5}, response)
			default:
				require.Error(t, err)
			}
			if !tc.retry {
				select {
				case <-freshRequest:
					t.Fatal("signing request was replayed")
				default:
				}
				response, err = proxy.exchange(ctx, []byte{11})
				require.NoError(t, err)
				assert.Equal(t, []byte{12, 0, 0, 0, 0}, response)
			}
			select {
			case request := <-freshRequest:
				assert.Equal(t, []byte{11}, request)
			case <-ctx.Done():
				t.Fatal("request never reached the fresh connection")
			}
		})
	}
}

func FuzzPermitted(f *testing.F) {
	f.Add(signingRequest("git"))
	f.Add([]byte{11})
	f.Fuzz(func(_ *testing.T, data []byte) { permitted(data) })
}

func TestQueuedRequestExpiresBeforeSigning(t *testing.T) {
	proxy := &signingProxy{requests: make(chan struct{}, 1)}
	proxy.requests <- struct{}{} // A slow signing request owns the connection.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := proxy.exchange(ctx, signingRequest("git"))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	<-proxy.requests
	// Even if the slot becomes available, an expired request is not sent.
	_, err = proxy.exchange(ctx, signingRequest("git"))
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
