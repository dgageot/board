package board

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// agentsDir is where agent YAML configs live.
func agentsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".agents"
	}
	return filepath.Join(home, ".agents")
}

// handleListAgents returns the YAML configs found directly under ~/.agents.
func (b *Board) handleListAgents(w http.ResponseWriter, _ *http.Request) {
	dir := agentsDir()

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, []string{})
			return
		}
		writeError(w, fmt.Errorf("read agents dir: %w", err))
		return
	}

	agents := []string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := strings.ToLower(filepath.Ext(e.Name())); ext == ".yaml" || ext == ".yml" {
			agents = append(agents, filepath.Join(dir, e.Name()))
		}
	}
	slices.Sort(agents)

	writeJSON(w, agents)
}

// browseResponse is the folder listing returned by handleBrowse.
type browseResponse struct {
	Path   string   `json:"path"`
	Parent string   `json:"parent"`
	Dirs   []string `json:"dirs"`
}

// handleBrowse lists the subdirectories of the requested path so the UI can
// offer a server-side folder picker. It defaults to the user's home directory
// and refuses to leave it: the picker exists to select project repositories,
// not to expose the whole filesystem to any page that can reach the API.
func (b *Board) handleBrowse(w http.ResponseWriter, r *http.Request) {
	home, err := os.UserHomeDir()
	if err != nil {
		writeError(w, fmt.Errorf("home dir: %w", err))
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		path = home
	}

	path = filepath.Clean(path)
	if !withinDir(home, path) {
		writeError(w, fmt.Errorf("%w: %s is outside the home directory", errBadInput, path))
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		writeError(w, fmt.Errorf("%w: read dir %s", errBadInput, path))
		return
	}

	dirs := []string{}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, e.Name())
		}
	}
	slices.Sort(dirs)

	parent := filepath.Dir(path)
	if parent == path || !withinDir(home, parent) {
		parent = ""
	}

	writeJSON(w, browseResponse{Path: path, Parent: parent, Dirs: dirs})
}

// withinDir reports whether path is root itself or nested somewhere below it.
// Both paths must already be absolute and clean.
func withinDir(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
