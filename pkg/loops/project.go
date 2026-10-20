package loops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ProjectSnapshot records the exact committed context supplied to an attempt.
type ProjectSnapshot struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Path     string        `json:"path"`
	Revision string        `json:"revision"`
	Digest   string        `json:"digest"`
	Files    []ProjectFile `json:"files"`
	Omitted  int           `json:"omitted"`
}

// ProjectFile contains a regular text blob, never working-tree content.
type ProjectFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

const maxProjectContext = 96 * 1024

// Project resolves only saved Board projects; request-supplied paths are never used.
func (s *Store) Project(ctx context.Context, id string) (ProjectSnapshot, error) {
	if id == "" {
		return ProjectSnapshot{}, fmt.Errorf("%w: choose a project for this run", ErrInvalid)
	}
	var project ProjectSnapshot
	err := s.db.QueryRowContext(ctx, "SELECT id,name,repo_path FROM projects WHERE id=?", id).Scan(&project.ID, &project.Name, &project.Path)
	if err != nil {
		return ProjectSnapshot{}, notFound(err)
	}
	project.Path, err = filepath.Abs(project.Path)
	if err != nil {
		return ProjectSnapshot{}, fmt.Errorf("%w: resolve project path: %w", ErrInvalid, err)
	}
	return project, nil
}

func (s *Store) projectSnapshot(ctx context.Context, id string) (*ProjectSnapshot, error) {
	project, err := s.Project(ctx, id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	root, err := projectGit(ctx, project.Path, 4096, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%w: project repository is unavailable: %w", ErrInvalid, err)
	}
	actual, err := filepath.EvalSymlinks(strings.TrimSpace(string(root)))
	if err != nil {
		return nil, err
	}
	selected, err := filepath.EvalSymlinks(project.Path)
	if err != nil {
		return nil, err
	}
	if actual != selected {
		return nil, fmt.Errorf("%w: select the repository root as the project; snapshots of repository subdirectories are not supported", ErrInvalid)
	}
	head, err := projectGit(ctx, project.Path, 256, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, fmt.Errorf("%w: project needs an accessible Git repository with a commit: %w", ErrInvalid, err)
	}
	project.Revision = strings.TrimSpace(string(head))
	listing, err := projectGit(ctx, project.Path, 2*1024*1024, "ls-tree", "-rzl", "--full-tree", project.Revision)
	if err != nil {
		return nil, fmt.Errorf("%w: list project files: %w", ErrInvalid, err)
	}
	type blob struct {
		path, oid string
	}
	var blobs []blob
	for entry := range strings.SplitSeq(string(listing), "\x00") {
		if entry == "" {
			continue
		}
		project.Omitted++
		metadata, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 4 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") || !projectTextFile(name) {
			continue
		}
		size, err := strconv.Atoi(fields[3])
		if err != nil || size > 16*1024 {
			continue
		}
		blobs = append(blobs, blob{name, fields[2]})
	}
	slices.SortStableFunc(blobs, func(a, b blob) int {
		if projectFilePriority(a.path) != projectFilePriority(b.path) {
			return projectFilePriority(a.path) - projectFilePriority(b.path)
		}
		return strings.Compare(a.path, b.path)
	})
	project.Files = []ProjectFile{}
	for _, blob := range blobs {
		if len(project.Files) == 24 {
			break
		}
		content, err := projectGit(ctx, project.Path, 16*1024, "cat-file", "blob", blob.oid)
		if err != nil {
			return nil, fmt.Errorf("read project blob: %w", err)
		}
		if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
			continue
		}
		project.Files = append(project.Files, ProjectFile{Path: blob.path, Content: string(content)})
		data, err := json.Marshal(project)
		if err != nil {
			return nil, err
		}
		if len(data) > maxProjectContext-128 {
			project.Files = project.Files[:len(project.Files)-1]
			continue
		}
		project.Omitted--
	}
	if len(project.Files) == 0 {
		return nil, fmt.Errorf("%w: project contains no supported committed text files", ErrInvalid)
	}
	data, err := json.Marshal(project)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	project.Digest = hex.EncodeToString(digest[:])
	return &project, nil
}

func projectFilePriority(name string) int {
	switch strings.ToLower(path.Base(name)) {
	case "readme.md", "readme", "agents.md":
		return 0
	case "go.mod", "package.json", "cargo.toml", "pyproject.toml":
		return 1
	default:
		return 2
	}
}

func projectTextFile(name string) bool {
	for part := range strings.SplitSeq(strings.ToLower(name), "/") {
		if strings.HasPrefix(part, ".") || strings.Contains(part, "secret") || strings.Contains(part, "credential") || part == "vendor" || part == "node_modules" {
			return false
		}
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".txt", ".go", ".js", ".ts", ".jsx", ".tsx", ".py", ".rs", ".java", ".c", ".h", ".css", ".html", ".toml":
		return true
	default:
		return path.Base(name) == "go.mod" || path.Base(name) == "package.json"
	}
}

type limitedOutput struct {
	bytes.Buffer

	limit int
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("project output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func projectGit(ctx context.Context, dir string, limit int, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager", "--no-replace-objects"}, args...)...)
	cmd.Dir = dir
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_LAZY_FETCH=1")
	cmd.WaitDelay = time.Second
	stdout := &limitedOutput{limit: limit}
	stderr := &limitedOutput{limit: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %s: %w", args[0], strings.TrimSpace(stderr.String()), err)
	}
	return stdout.Bytes(), nil
}
