package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxCardAttachments     = 10
	maxCardAttachmentBytes = 20 << 20
	maxCardUploadBytes     = maxCardAttachmentBytes + (1 << 20) // Leave room for the prompt and multipart headers.
)

func readCardUpload(w http.ResponseWriter, r *http.Request) (*createCardRequest, []*multipart.FileHeader, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCardUploadBytes)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) || errors.Is(err, multipart.ErrMessageTooLarge) {
			http.Error(w, "Task upload is too large (20 MiB total for attachments).", http.StatusRequestEntityTooLarge)
		} else {
			writeError(w, fmt.Errorf("%w: invalid task upload", errBadInput))
		}
		return nil, nil, false
	}

	form := r.MultipartForm
	files := form.File["attachments"]
	if len(files) > maxCardAttachments {
		writeError(w, fmt.Errorf("%w: attach at most %d files", errBadInput, maxCardAttachments))
		return nil, nil, false
	}
	var total int64
	for _, file := range files {
		total += file.Size
	}
	if total > maxCardAttachmentBytes {
		http.Error(w, "Attachments exceed 20 MiB total.", http.StatusRequestEntityTooLarge)
		return nil, nil, false
	}
	var req createCardRequest
	if err := json.Unmarshal([]byte(r.PostFormValue("task")), &req); err != nil {
		writeError(w, fmt.Errorf("%w: invalid task metadata", errBadInput))
		return nil, nil, false
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, fmt.Errorf("%w: prompt required", errBadInput))
		return nil, nil, false
	}
	return &req, files, true
}

func cardAttachmentDir(cardID string) (string, error) {
	if !filepath.IsLocal(cardID) || filepath.Base(cardID) != cardID || cardID == "." {
		return "", errors.New("invalid card ID for attachment directory")
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(config, "board", "attachments", cardID), nil
}

// stageCardAttachments keeps user files out of the worktree and returns a
// prompt suffix pointing the agent to each private copy.
func stageCardAttachments(cardID string, files []*multipart.FileHeader) (string, error) {
	dir, err := cardAttachmentDir(cardID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}

	paths := make([]string, 0, len(files))
	for i, file := range files {
		name := filepath.Base(strings.ReplaceAll(file.Filename, `\`, "/"))
		path := filepath.Join(dir, fmt.Sprintf("%02d-%s", i+1, attachmentFilename(name)))
		if err := copyCardAttachment(path, file); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
		paths = append(paths, path)
	}
	return attachmentsSuffix(paths), nil
}

// stagedAttachmentsSuffix rebuilds the prompt suffix for the files already
// staged for a card, so a first launch that never ran can be retried with the
// same attachments. Staged names are numbered, so directory order is upload
// order.
func stagedAttachmentsSuffix(cardID string) string {
	dir, err := cardAttachmentDir(cardID)
	if err != nil {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	return attachmentsSuffix(paths)
}

func attachmentsSuffix(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var suffix strings.Builder
	suffix.WriteString("\n\nFiles attached to this task (use your tools to read them as needed):\n")
	for _, path := range paths {
		fmt.Fprintf(&suffix, "- %q\n", path)
	}
	return suffix.String()
}

func attachmentFilename(name string) string {
	// Keep common extensions, but never use a browser filename as a path.
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r) {
			return r
		}
		return '_'
	}, name)
	if len(name) > 240 {
		return name[:240]
	}
	return name
}

func copyCardAttachment(path string, file *multipart.FileHeader) error {
	src, err := file.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(dst, src)
	return errors.Join(copyErr, dst.Close())
}

func removeCardAttachments(cardID string) error {
	dir, err := cardAttachmentDir(cardID)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func cleanupCardAttachments(cardID string) {
	if err := removeCardAttachments(cardID); err != nil {
		log.Printf("card %s: remove attachments: %v", cardID, err)
	}
}
