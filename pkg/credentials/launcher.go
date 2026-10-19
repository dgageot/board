package credentials

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// InstallLauncher keeps a private executable for card launches even if the
// original Board binary is removed (for example, by Go's build cache cleanup).
func (s *Server) InstallLauncher(source string) (string, error) {
	file, err := os.Open(source)
	if err != nil {
		return "", fmt.Errorf("open credential launcher: %w", err)
	}
	defer func() { _ = file.Close() }()
	// Temporary directories can be mounted noexec. Keep executable code in the
	// user cache, with a separate private directory for each helper socket.
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate launcher cache: %w", err)
	}
	dir := filepath.Join(cache, "board", "launchers", fmt.Sprintf("%x", sha256.Sum256([]byte(s.Socket))))
	lock, err := lockDirectory(dir)
	if err != nil {
		return "", fmt.Errorf("lock launcher cache: %w", err)
	}
	defer func() { _ = lock.Close() }()
	path := filepath.Join(dir, "board")
	if err := writeExecutable(path, file); err != nil {
		return "", fmt.Errorf("install credential launcher: %w", err)
	}
	return path, nil
}

// Publish atomically: surviving cards may execute helpers during a restart.
func writeExecutable(path string, content io.Reader) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".helper-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()
	if _, err := io.Copy(file, content); err != nil {
		return err
	}
	if err := file.Chmod(0o700); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
