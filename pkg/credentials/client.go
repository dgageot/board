package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Exec obtains credentials directly from Board, then replaces this process.
// Only the socket path and command (never secret values) appear in tmux history.
func Exec(socket string, args []string) error {
	if len(args) == 0 {
		return errors.New("credential exec needs a command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout+5*time.Second)
	defer cancel()
	env, err := fetchEnvironment(ctx, socket)
	if err != nil {
		return err
	}
	if env.Warning != "" {
		log.Print(env.Warning)
	}
	values, err := commandEnvironment(os.Environ(), env)
	if err != nil {
		return err
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, args, values)
}

func fetchEnvironment(ctx context.Context, socket string) (environment, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://board/env", http.NoBody)
	if err != nil {
		return environment{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return environment{}, errors.New("board credential helper is unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return environment{}, errors.New("board could not load credentials; check 1Password and restart Board to retry")
	}
	var env environment
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxMessage)).Decode(&env); err != nil {
		return environment{}, errors.New("invalid credential helper response")
	}
	return env, nil
}

func commandEnvironment(current []string, shared environment) ([]string, error) {
	values := make(map[string]string)
	for _, entry := range current {
		if key, value, ok := strings.Cut(entry, "="); ok {
			// Never let stale tmux references trigger per-card lookups. Only
			// Board's current, resolved references are injected below.
			delete(values, key)
			if !strings.HasPrefix(value, "op://") {
				values[key] = value
			}
		}
	}
	if shared.AuthSocket != nil {
		delete(values, "SSH_AUTH_SOCK")
		if *shared.AuthSocket != "" {
			values["SSH_AUTH_SOCK"] = *shared.AuthSocket
		}
	}
	// Resolved references (including empty failed lookups) take precedence.
	maps.Copy(values, shared.Values)
	if shared.SSHSocket != "" {
		// Append to Git's process-local config, preserving any existing entries.
		// A plain environment variable cannot override global op-ssh-sign.
		count := 0
		if raw := values["GIT_CONFIG_COUNT"]; raw != "" {
			var err error
			count, err = strconv.Atoi(raw)
			if err != nil || count < 0 || count > 10000 {
				return nil, errors.New("invalid inherited GIT_CONFIG_COUNT")
			}
		}
		values[fmt.Sprintf("GIT_CONFIG_KEY_%d", count)] = "gpg.ssh.program"
		values[fmt.Sprintf("GIT_CONFIG_VALUE_%d", count)] = shared.Signer
		values["GIT_CONFIG_COUNT"] = strconv.Itoa(count + 1)
	}
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	slices.Sort(env)
	return env, nil
}
