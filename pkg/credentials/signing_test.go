package credentials

import (
	"crypto/subtle"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitSigning(t *testing.T) {
	for _, binary := range []string{"git", "ssh-agent", "ssh-add", "ssh-keygen"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip(binary + " is not installed")
		}
	}
	dir := socketDir(t)
	key := filepath.Join(dir, "key")
	run(t, nil, "", "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
	public, err := os.ReadFile(key + ".pub")
	require.NoError(t, err)
	upstream := filepath.Join(dir, "upstream.sock")
	cmd := exec.Command("ssh-agent", "-D", "-a", upstream)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	require.Eventually(t, func() bool {
		_, err := os.Stat(upstream)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	env := append(os.Environ(), "SSH_AUTH_SOCK="+upstream)
	run(t, env, "", "ssh-add", key)
	require.NoError(t, os.Remove(key)) // Only the agent has the private key now.

	s, err := Start(t.Context(), filepath.Join(dir, "helper with spaces"), upstream, env)
	require.NoError(t, err)
	defer s.Close()
	shared, err := fetchEnvironment(t.Context(), s.Socket)
	require.NoError(t, err)
	childEnv, err := commandEnvironment(env, shared)
	require.NoError(t, err)
	// Authentication still reaches the original agent, not the Git-only proxy.
	run(t, childEnv, "", "ssh-add", "-T", key+".pub")
	verifyCommits(t, dir, childEnv, strings.TrimSpace(string(public)))

	// Existing cards keep the stable script/socket paths through Board restarts.
	s.Close()
	s, err = Start(t.Context(), filepath.Join(dir, "helper with spaces"), upstream, nil)
	require.NoError(t, err)
	defer s.Close()
	run(t, childEnv, filepath.Join(dir, "repo"), "git", "commit", "--quiet", "--allow-empty", "-m", "after restart")
	run(t, childEnv, filepath.Join(dir, "repo"), "git", "verify-commit", "HEAD")
}

// Opt in explicitly: this signs disposable commits using the user's real key
// and may trigger a 1Password dialog. Ordinary test runs use a synthetic agent.
func TestLive1PasswordGitSigning(t *testing.T) {
	if os.Getenv("BOARD_TEST_1PASSWORD") != "1" {
		t.Skip("set BOARD_TEST_1PASSWORD=1 for live 1Password verification")
	}
	source := os.Getenv("SSH_AUTH_SOCK")
	require.NotEmpty(t, source)
	key := strings.TrimSpace(run(t, nil, "", "git", "config", "--global", "--get", "user.signingkey"))
	require.True(t, strings.HasPrefix(key, "key::ssh-"), "live verification requires an inline public signing key")
	dir := socketDir(t)
	s, err := Start(t.Context(), filepath.Join(dir, "helper"), source, nil)
	require.NoError(t, err)
	defer s.Close()
	shared, err := fetchEnvironment(t.Context(), s.Socket)
	require.NoError(t, err)
	env, err := commandEnvironment(os.Environ(), shared)
	require.NoError(t, err)
	verifyCommits(t, dir, env, strings.TrimPrefix(key, "key::"))
}

func verifyCommits(t *testing.T, dir string, env []string, public string) {
	t.Helper()
	repo := filepath.Join(dir, "repo")
	require.NoError(t, os.Mkdir(repo, 0o700))
	allowed := filepath.Join(dir, "allowed_signers")
	require.NoError(t, os.WriteFile(allowed, []byte("* namespaces=\"git\" "+public+"\n"), 0o600))
	// Deliberately configure a broken normal signer: the process-local override
	// must win without touching user/global or shared repository configuration.
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	run(t, env, repo, "git", "init", "--quiet")
	for name, value := range map[string]string{
		"user.name": "Board signing test", "user.email": "board-test@example.invalid",
		"user.signingkey": "key::" + public, "gpg.format": "ssh",
		"gpg.ssh.program": "/nonexistent-signer", "commit.gpgsign": "true",
		"gpg.ssh.allowedSignersFile": allowed,
	} {
		run(t, env, repo, "git", "config", "--local", name, value)
	}
	for _, message := range []string{"first signature", "second signature"} {
		run(t, env, repo, "git", "commit", "--quiet", "--allow-empty", "-m", message)
		result := run(t, env, repo, "git", "verify-commit", "HEAD")
		assert.Contains(t, result, "Good \"git\" signature")
	}
	t.Log("two real Git commits signed through the Go helper and verified")
}

func run(t *testing.T, env []string, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Env = env
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s %v: %s", name, args, out)
	return string(out)
}

func TestCredentialExec(t *testing.T) {
	dir := socketDir(t)
	s, err := Start(t.Context(), dir+"/helper", "", nil)
	require.NoError(t, err)
	defer s.Close()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCredentialExecChild$")
	cmd.Env = append(os.Environ(), "BOARD_EXEC_CHILD_SOCKET="+s.Socket, "TEST_TOKEN=literal with 'quotes' and $symbols", "STALE_TOKEN=op://must-not-leak")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Equal(t, "literal with 'quotes' and $symbols:unset", string(out))
}

func TestCredentialExecChild(t *testing.T) {
	socket := os.Getenv("BOARD_EXEC_CHILD_SOCKET")
	if socket == "" {
		t.Skip("subprocess only")
	}
	err := Exec(socket, []string{"sh", "-c", `printf '%s:%s' "$TEST_TOKEN" "${STALE_TOKEN-unset}"`})
	require.NoError(t, err)
}

func TestLiveSecretSharing(t *testing.T) {
	name := os.Getenv("BOARD_TEST_SECRET")
	if name == "" {
		t.Skip("set BOARD_TEST_SECRET to an environment variable containing a secret reference")
	}
	reference := os.Getenv(name)
	require.True(t, strings.HasPrefix(reference, "op://"), "live verification requires a secret reference")
	s, err := Start(t.Context(), socketDir(t)+"/helper", "", []string{name + "=" + reference})
	require.NoError(t, err)
	defer s.Close()
	first, err := fetchEnvironment(t.Context(), s.Socket)
	require.NoError(t, err)
	require.Empty(t, first.Warning)
	value := first.Values[name]
	require.True(t, value != "" && !strings.HasPrefix(value, "op://"), "expected a resolved credential")
	second, err := fetchEnvironment(t.Context(), s.Socket)
	require.NoError(t, err)
	// Compare without including credential values in failure messages.
	require.Equal(t, 1, subtle.ConstantTimeCompare([]byte(value), []byte(second.Values[name])), "expected the cached credential")
	t.Log("credential resolved and reused without exposing its value")
}
