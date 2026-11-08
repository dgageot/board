package credentials

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func installKeyReader(t *testing.T, dir string) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(key, "board-test")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "key"), pem.EncodeToMemory(block), 0o600))
	public, err := ssh.NewPublicKey(key.Public())
	require.NoError(t, err)
	t.Setenv("KEY_SOURCE", filepath.Join(dir, "key"))
	t.Setenv("KEY_READ_CALLS", filepath.Join(dir, "calls"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "op"), []byte("#!/bin/sh\nprintf x >> \"$KEY_READ_CALLS\"\ncat \"$KEY_SOURCE\"\n"), 0o700))
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public)))
}

func TestOwnedAgentSignsWithoutFurtherSecretReads(t *testing.T) {
	dir := socketDir(t)
	public := installKeyReader(t, dir)
	reference := "BOARD_SSH_KEY=op://test/signing/private key?ssh-format=openssh"
	inherited := []string{reference, "SSH_AUTH_SOCK=/original/auth.sock"}
	s, err := Start(t.Context(), dir+"/helper", "/unavailable/1password.sock", inherited)
	require.NoError(t, err)
	defer s.Close()
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	require.NoError(t, err)
	assert.Equal(t, "x", string(calls), "key must load at startup")
	require.NoError(t, os.Remove(filepath.Join(dir, "key")))

	shared, err := fetchEnvironment(t.Context(), s.Socket, inherited)
	require.NoError(t, err)
	assert.Empty(t, shared.Values)
	assert.Empty(t, shared.Warning)
	childEnv, err := commandEnvironment(append(os.Environ(), inherited...), shared)
	require.NoError(t, err)
	assert.Contains(t, childEnv, "SSH_AUTH_SOCK=/original/auth.sock")
	assert.NotContains(t, strings.Join(childEnv, "\n"), "BOARD_SSH_KEY=")
	verifyCommits(t, dir, childEnv, public)

	conn, err := net.Dial("unix", shared.SSHSocket)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	client := agent.NewClient(conn)
	keys, err := client.List()
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, public, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(keys[0]))))
	_, err = client.Sign(keys[0], []byte("not a Git signature"))
	require.Error(t, err, "ordinary SSH authentication must remain blocked")
	require.Error(t, client.RemoveAll(), "clients must not edit the keyring")
	require.Error(t, client.Lock([]byte("passphrase")), "clients must not lock the keyring")
	keys, err = client.List()
	require.NoError(t, err)
	assert.Len(t, keys, 1)
	calls, err = os.ReadFile(filepath.Join(dir, "calls"))
	require.NoError(t, err)
	assert.Equal(t, "x", string(calls))

	keyring := s.proxy.keyring
	s.Close()
	keys, err = keyring.List()
	require.NoError(t, err)
	assert.Empty(t, keys)
	_, err = os.Stat(shared.SSHSocket)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = fetchEnvironment(t.Context(), s.Socket, nil)
	require.Error(t, err)
}

func TestOwnedAgentResumesSigningAfterBoardRestart(t *testing.T) {
	dir := socketDir(t)
	public := installKeyReader(t, dir)
	inherited := []string{"BOARD_SSH_KEY=op://test/signing/private key?ssh-format=openssh"}
	s, err := Start(t.Context(), dir+"/helper", "", inherited)
	require.NoError(t, err)
	defer s.Close()
	shared, err := fetchEnvironment(t.Context(), s.Socket, nil)
	require.NoError(t, err)
	childEnv, err := commandEnvironment(os.Environ(), shared)
	require.NoError(t, err)
	verifyCommits(t, dir, childEnv, public)
	s.Close()
	s, err = Start(t.Context(), dir+"/helper", "", inherited)
	require.NoError(t, err)
	defer s.Close()
	run(t, childEnv, filepath.Join(dir, "repo"), "git", "commit", "--quiet", "--allow-empty", "-m", "after restart")
	run(t, childEnv, filepath.Join(dir, "repo"), "git", "verify-commit", "HEAD")
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	require.NoError(t, err)
	assert.Equal(t, "xx", string(calls))
}

func TestOwnedAgentClearedOnContextCancellation(t *testing.T) {
	dir := socketDir(t)
	installKeyReader(t, dir)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s, err := Start(ctx, dir+"/helper", "", []string{"BOARD_SSH_KEY=op://test/key/private key"})
	require.NoError(t, err)
	defer s.Close()
	keyring := s.proxy.keyring
	cancel()
	require.Eventually(t, func() bool {
		keys, err := keyring.List()
		return err == nil && len(keys) == 0
	}, 5*time.Second, 10*time.Millisecond)
	s.Close()
	_, err = os.Stat(s.Socket)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestOwnedAgentStartupFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reference string
		script    string
		message   string
	}{
		{"literal", "sensitive-private-key", "exit 99", "must be an op:// reference"},
		{"lookup failure", "op://test/key/private key", "echo sensitive-output; echo sensitive-error >&2; exit 1", "cannot resolve"},
		{"empty", "op://test/key/private key", "exit 0", "unencrypted SSH private key"},
		{"malformed", "op://test/key/private key", "echo sensitive-output", "unencrypted SSH private key"},
		{"encrypted", "op://test/key/private key", "cat \"$KEY_SOURCE\"", "unencrypted SSH private key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := socketDir(t)
			installKeyReader(t, dir)
			if tc.name == "encrypted" {
				run(t, nil, "", "ssh-keygen", "-q", "-p", "-P", "", "-N", "test-passphrase", "-f", filepath.Join(dir, "key"))
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, "op"), []byte("#!/bin/sh\n"+tc.script+"\n"), 0o700))
			s, err := Start(t.Context(), dir+"/helper", "/upstream.sock", []string{"BOARD_SSH_KEY=" + tc.reference})
			require.ErrorContains(t, err, tc.message)
			assert.NotContains(t, err.Error(), "sensitive")
			assert.Nil(t, s)
			_, err = os.Stat(dir + "/helper/agent.sock")
			require.ErrorIs(t, err, os.ErrNotExist)
			s, err = Start(t.Context(), dir+"/helper", "", nil)
			require.NoError(t, err, "failed startup must release the directory lock")
			s.Close()
		})
	}
}

func TestSSHKeyNeverSharedAsEnvironment(t *testing.T) {
	inherited := []string{"BOARD_SSH_KEY=op://test/key/private key", "TOKEN=op://test/token"}
	assert.Equal(t, map[string]string{"TOKEN": "op://test/token"}, secretReferences(inherited))
	assert.Empty(t, literalEnvironment([]string{"BOARD_SSH_KEY=sensitive-key"}))
	for _, value := range []string{"", "op://stale/key", "sensitive-key"} {
		env, err := commandEnvironment([]string{"BOARD_SSH_KEY=" + value, "SAFE=preserved"}, environment{})
		require.NoError(t, err)
		assert.Equal(t, []string{"SAFE=preserved"}, env)
	}
	env, err := commandEnvironment(nil, environment{Values: map[string]string{"BOARD_SSH_KEY": "sensitive-key"}})
	require.NoError(t, err)
	assert.Empty(t, env)
}

func TestSSHKeyAliasRejectedBeforeResolution(t *testing.T) {
	for _, alias := range []string{
		"op://test/key/private key?ssh-format=openssh",
		"op://test/key/private key",
		"op://test/key/private key?ssh-format=pkcs8",
	} {
		t.Run(alias, func(t *testing.T) {
			dir := socketDir(t)
			installKeyReader(t, dir)
			s, err := Start(t.Context(), dir+"/helper", "", []string{
				"BOARD_SSH_KEY=op://test/key/private key?ssh-format=openssh", "ALIAS=" + alias,
			})
			require.ErrorContains(t, err, "must not be shared")
			assert.Nil(t, s)
			_, err = os.Stat(filepath.Join(dir, "calls"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestSSHKeyLastEnvironmentValueWins(t *testing.T) {
	inherited := []string{"BOARD_SSH_KEY=op://old/key", "BOARD_SSH_KEY="}
	assert.Empty(t, sshKeyReference(inherited))
	s, err := Start(t.Context(), socketDir(t)+"/helper", "", inherited)
	require.NoError(t, err)
	defer s.Close()
	assert.Nil(t, s.proxy)
}
