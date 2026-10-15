package credentials

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func socketDir(t *testing.T) string {
	t.Helper()
	// macOS's default test directory can exceed the Unix socket path limit.
	t.Setenv("TMPDIR", "/tmp")
	return t.TempDir()
}

func TestSecretReferencesResolvedOnce(t *testing.T) {
	dir := socketDir(t)
	counter := filepath.Join(dir, "calls")
	t.Setenv("COUNTER", counter)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "op"), []byte("#!/bin/sh\nprintf x >> \"$COUNTER\"\nprintf 'fake-shared-key'\n"), 0o700))
	s, err := Start(t.Context(), dir+"/helper", "", []string{
		"SEARCH_TOKEN=op://test/search/credential",
		"SEARCH_ALIAS=op://test/search/credential",
		"OTHER_TOKEN=op://test/other/credential",
		"LITERAL=not-a-reference",
	})
	require.NoError(t, err)
	defer s.Close()
	_, err = os.Stat(counter)
	require.ErrorIs(t, err, os.ErrNotExist, "credentials should load lazily")
	errors := make(chan error, 10)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			env, err := fetchEnvironment(t.Context(), s.Socket)
			errors <- err
			assert.Equal(t, map[string]string{
				"SEARCH_TOKEN": "fake-shared-key", "SEARCH_ALIAS": "fake-shared-key", "OTHER_TOKEN": "fake-shared-key",
			}, env.Values)
		})
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	calls, err := os.ReadFile(counter)
	require.NoError(t, err)
	assert.Equal(t, "xx", string(calls))
}

func TestSecretFailureDoesNotBlockSigningOrOtherSecrets(t *testing.T) {
	dir := socketDir(t)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "op"), []byte("#!/bin/sh\ncase \"$3\" in *good*) printf resolved; exit 0;; esac\necho sensitive-output\necho sensitive-error >&2\nexit 1\n"), 0o700))
	s, err := Start(t.Context(), dir+"/helper", dir+"/upstream", []string{
		"BROKEN_TOKEN=op://test/broken/credential", "GOOD_TOKEN=op://test/good/credential",
	})
	require.NoError(t, err)
	defer s.Close()
	env, err := fetchEnvironment(t.Context(), s.Socket)
	require.NoError(t, err)
	assert.NotEmpty(t, env.Signer)
	assert.Contains(t, env.Warning, "Some credentials are unavailable")
	assert.NotContains(t, env.Warning, "sensitive")
	assert.Contains(t, env.Values, "BROKEN_TOKEN")
	assert.Empty(t, env.Values["BROKEN_TOKEN"])
	assert.Equal(t, "resolved", env.Values["GOOD_TOKEN"])
	child, err := commandEnvironment([]string{"BROKEN_TOKEN=op://test/broken/credential"}, env)
	require.NoError(t, err)
	assert.Contains(t, child, "BROKEN_TOKEN=")
}

func TestServerLockPermissionsAndRestart(t *testing.T) {
	dir := socketDir(t) + "/helper"
	s, err := Start(t.Context(), dir, "", []string{"LITERAL=literal-test-key"})
	require.NoError(t, err)
	defer s.Close()
	info, err := os.Stat(s.Socket)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	_, err = Start(t.Context(), dir, "", nil)
	require.ErrorContains(t, err, "already running")
	s.Close()
	s, err = Start(t.Context(), dir, "", nil)
	require.NoError(t, err)
	defer s.Close()
	_, err = fetchEnvironment(t.Context(), s.Socket)
	require.NoError(t, err)
}

func TestRejectUnsafeDirectoryAndSocket(t *testing.T) {
	dir := socketDir(t)
	require.NoError(t, os.Mkdir(dir+"/public", 0o755))
	_, err := Start(t.Context(), dir+"/public", "", nil)
	require.ErrorContains(t, err, "private")
	require.NoError(t, os.Symlink(dir, dir+"/link"))
	_, err = Start(t.Context(), dir+"/link", "", nil)
	require.Error(t, err)
	require.NoError(t, os.Mkdir(dir+"/private", 0o700))
	require.NoError(t, os.WriteFile(dir+"/private/env.sock", []byte("keep"), 0o600))
	_, err = Start(t.Context(), dir+"/private", "", nil)
	require.ErrorContains(t, err, "non-socket")
	content, err := os.ReadFile(dir + "/private/env.sock")
	require.NoError(t, err)
	assert.Equal(t, "keep", string(content))
}

func TestCommandEnvironment(t *testing.T) {
	shared := environment{SSHSocket: "/private/agent", Signer: "'/private/sign helper'", Values: map[string]string{"SEARCH_TOKEN": "resolved"}}
	current := []string{"SSH_AUTH_SOCK=/original", "SEARCH_TOKEN=op://test", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=test.name", "GIT_CONFIG_VALUE_0=preserved"}
	result, err := commandEnvironment(current, shared)
	require.NoError(t, err)
	assert.Contains(t, result, "SSH_AUTH_SOCK=/original", "SSH authentication must keep the original agent")
	assert.Contains(t, result, "SEARCH_TOKEN=resolved")
	assert.Contains(t, result, "GIT_CONFIG_COUNT=2")
	assert.Contains(t, result, "GIT_CONFIG_VALUE_0=preserved")
	assert.Contains(t, result, "GIT_CONFIG_KEY_1=gpg.ssh.program")
	assert.Contains(t, result, "GIT_CONFIG_VALUE_1='/private/sign helper'")
	_, err = commandEnvironment([]string{"GIT_CONFIG_COUNT=not-a-number"}, shared)
	require.Error(t, err)
	_, err = commandEnvironment([]string{"GIT_CONFIG_COUNT=-1"}, shared)
	require.Error(t, err)
}

func TestCredentialsAbsentPreservesSigner(t *testing.T) {
	result, err := commandEnvironment([]string{"SSH_AUTH_SOCK=/original", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=gpg.ssh.program", "GIT_CONFIG_VALUE_0=my-signer"}, environment{})
	require.NoError(t, err)
	assert.Contains(t, result, "GIT_CONFIG_VALUE_0=my-signer")
	assert.Contains(t, result, "GIT_CONFIG_COUNT=1")
	assert.NotContains(t, strings.Join(result, "\n"), "SEARCH_TOKEN")
}

func TestMissingReferencesClearStaleTmuxEnvironment(t *testing.T) {
	s, err := Start(t.Context(), socketDir(t)+"/helper", "", nil)
	require.NoError(t, err)
	defer s.Close()
	shared, err := fetchEnvironment(t.Context(), s.Socket)
	require.NoError(t, err)
	env, err := commandEnvironment([]string{"OLD_TOKEN=op://old/token", "OTHER_TOKEN=op://old/other", "LITERAL=preserved"}, shared)
	require.NoError(t, err)
	assert.Equal(t, []string{"LITERAL=preserved"}, env)
}

func TestSecretReferencesSnapshot(t *testing.T) {
	inherited := []string{
		"TOKEN=op://old/value", "TOKEN=literal-wins", "OTHER=literal", "OTHER=op://new/value",
		"LOWER=op://case/value", "UPPER=OP://not-a-reference", "EMPTY=", "NO_EQUALS", "=op://invalid",
	}
	assert.Equal(t, map[string]string{"OTHER": "op://new/value", "LOWER": "op://case/value"}, secretReferences(inherited))
	s, err := Start(t.Context(), socketDir(t)+"/helper", "", inherited[:2])
	require.NoError(t, err)
	defer s.Close()
	inherited[1] = "TOKEN=op://must-not-be-read"
	shared, err := fetchEnvironment(t.Context(), s.Socket)
	require.NoError(t, err)
	assert.Empty(t, shared.Values)
	assert.Empty(t, shared.Warning)
}

func TestReferenceChangesAcrossRestart(t *testing.T) {
	dir := socketDir(t)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "op"), []byte("#!/bin/sh\nprintf 'resolved:%s' \"$3\"\n"), 0o700))
	inherited := []string{"TOKEN=op://old/value", "REMOVED=op://old/removed"}
	s, err := Start(t.Context(), dir+"/helper", "", inherited)
	require.NoError(t, err)
	defer s.Close()
	first, err := fetchEnvironment(t.Context(), s.Socket)
	require.NoError(t, err)
	assert.Len(t, first.Values, 2)
	s.Close()
	s, err = Start(t.Context(), dir+"/helper", "", []string{"TOKEN=op://new/value"})
	require.NoError(t, err)
	defer s.Close()
	second, err := fetchEnvironment(t.Context(), s.Socket)
	require.NoError(t, err)
	child, err := commandEnvironment(inherited, second)
	require.NoError(t, err)
	assert.Equal(t, []string{"TOKEN=resolved:op://new/value"}, child)
}

func TestSlowReferenceDoesNotBlockIndependentLookup(t *testing.T) {
	fastDone := make(chan struct{})
	timeout := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		values, failed := resolveReferences(t.Context(), map[string]string{"SLOW": "slow", "FAST": "fast"}, func(_ context.Context, reference string) (string, error) {
			if reference == "slow" {
				<-timeout
				return "", context.DeadlineExceeded
			}
			close(fastDone)
			return "resolved", nil
		})
		assert.True(t, failed)
		assert.Equal(t, map[string]string{"SLOW": "", "FAST": "resolved"}, values)
	}()
	select {
	case <-fastDone:
	case <-time.After(5 * time.Second):
		close(timeout)
		<-finished
		t.Fatal("independent lookup blocked behind slow reference")
	}
	close(timeout)
	<-finished
}
