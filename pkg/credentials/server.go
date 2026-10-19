// Package credentials shares ephemeral credentials and SSH signing across cards.
package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"al.essio.dev/pkg/shellescape"
)

// Server owns a private, stable socket directory so surviving cards can sign
// again when Board restarts. Plaintext secrets are kept only in process memory.
type Server struct {
	Socket    string
	proxy     *signingProxy
	listeners []net.Listener
	http      *http.Server
	lock      *os.File
	cancel    context.CancelFunc
}

type environment struct {
	Values     map[string]string `json:"values"`
	SSHSocket  string            `json:"sshSocket,omitempty"`
	AuthSocket *string           `json:"authSocket,omitempty"`
	Signer     string            `json:"signer,omitempty"`
	Warning    string            `json:"warning,omitempty"`
}

// Start starts the shared helper. source is the original SSH_AUTH_SOCK.
// Only op:// references from inherited are resolved and shared with cards.
func Start(ctx context.Context, dir, source string, inherited []string) (_ *Server, retErr error) {
	lock, err := lockDirectory(dir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{Socket: filepath.Join(dir, "env.sock"), lock: lock, cancel: cancel}
	defer func() {
		if retErr != nil {
			s.Close()
		}
	}()

	references := secretReferences(inherited)
	// tmux and shell startup files can retain an obsolete authentication socket.
	// An empty value explicitly removes it; older helpers omit this field.
	authSocket := ""
	for _, entry := range inherited {
		if value, ok := strings.CutPrefix(entry, "SSH_AUTH_SOCK="); ok {
			authSocket = value
		}
	}
	env := environment{Values: make(map[string]string), AuthSocket: &authSocket}
	if source != "" {
		env.SSHSocket = filepath.Join(dir, "agent.sock")
		if source == env.SSHSocket {
			return nil, errors.New("credential helper cannot proxy itself")
		}
		signer, err := exec.LookPath("ssh-keygen")
		if err != nil {
			return nil, fmt.Errorf("find SSH signer: %w", err)
		}
		// A stable script works even after a `go run` binary disappears. Only
		// the signing subprocess sees the proxy; SSH fetch/push keep their agent.
		script := filepath.Join(dir, "sign")
		content := "#!/bin/sh\nexec env SSH_AUTH_SOCK=" + shellescape.Quote(env.SSHSocket) + " " + shellescape.Quote(signer) + " \"$@\"\n"
		if err := writeExecutable(script, strings.NewReader(content)); err != nil {
			return nil, fmt.Errorf("write signing helper: %w", err)
		}
		env.Signer = script
		listener, err := s.listen(env.SSHSocket)
		if err != nil {
			return nil, err
		}
		s.proxy = &signingProxy{source: source, requests: make(chan struct{}, 1), clients: make(map[net.Conn]struct{})}
		go s.proxy.serve(ctx, listener)
	}

	// One resolution per distinct reference (including failures), even when
	// cards start concurrently. Never use a client's context for shared work.
	resolve := sync.OnceValue(func() environment {
		values, failed := resolveReferences(ctx, references, resolveSecret)
		env.Values = values
		if failed {
			env.Warning = "Some credentials are unavailable; check 1Password and restart Board to retry"
		}
		return env
	})
	listener, err := s.listen(s.Socket)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /env", func(w http.ResponseWriter, _ *http.Request) {
		resolved := resolve()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(resolved)
	})
	s.http = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	go func() { _ = s.http.Serve(listener) }()
	return s, nil
}

// secretReferences snapshots the effective environment without retaining literals.
func secretReferences(inherited []string) map[string]string {
	references := make(map[string]string)
	for _, entry := range inherited {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		if strings.HasPrefix(value, "op://") {
			references[name] = value
		} else {
			delete(references, name)
		}
	}
	return references
}

// Independent lookups share neither a timeout nor a failure. Aliases reuse the
// same result, and the enclosing OnceValue caches failures as well as successes.
func resolveReferences(ctx context.Context, references map[string]string, resolve func(context.Context, string) (string, error)) (map[string]string, bool) {
	type result struct {
		reference string
		value     string
		err       error
	}
	unique := make(map[string]struct{})
	for _, reference := range references {
		unique[reference] = struct{}{}
	}
	results := make(chan result, len(unique))
	for reference := range unique {
		go func() {
			ctx, cancel := context.WithTimeout(ctx, requestTimeout)
			defer cancel()
			value, err := resolve(ctx, reference)
			results <- result{reference: reference, value: value, err: err}
		}()
	}
	cache := make(map[string]string, len(unique))
	failed := false
	for range unique {
		result := <-results
		if result.err != nil {
			// An empty value prevents child agents from retrying failed references.
			result.value = ""
			failed = true
		}
		cache[result.reference] = result.value
	}
	values := make(map[string]string, len(references))
	for name, reference := range references {
		values[name] = cache[reference]
	}
	return values, failed
}

func resolveSecret(ctx context.Context, reference string) (string, error) {
	// Discard stderr: helper errors must never disclose credential material.
	out, err := exec.CommandContext(ctx, "op", "read", "--no-newline", reference).Output()
	if err != nil {
		return "", errors.New("cannot resolve credential through 1Password")
	}
	if len(out) == 0 {
		return "", errors.New("empty credential")
	}
	return string(out), nil
}

func lockDirectory(dir string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ok || stat.Uid != uint32(os.Getuid()) {
		return nil, errors.New("credential directory must be private and owned by the current user")
	}
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, errors.New("another board credential helper is already running")
	}
	return lock, nil
}

func (s *Server) listen(path string) (net.Listener, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("refusing to replace a non-socket credential path")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := listenSocket(path)
	if err != nil {
		return nil, err
	}
	s.listeners = append(s.listeners, listener)
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	return listener, nil
}

// Close revokes the sockets and releases the directory for the next Board run.
func (s *Server) Close() {
	s.cancel()
	if s.http != nil {
		_ = s.http.Close()
	}
	for _, listener := range s.listeners {
		_ = listener.Close()
	}
	if s.proxy != nil {
		s.proxy.close()
	}
	_ = s.lock.Close()
}
