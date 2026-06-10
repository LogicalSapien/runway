package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	dbpkg "github.com/LogicalSapien/runway/internal/db"
)

const maxBlobSize = 1 << 20 // 1 MiB — the UI is a quick checkout inspector, not an editor

// contentEntry mirrors the GitHub contents API shape (subset).
type contentEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"` // "file" | "dir" | "symlink"
	Size     int64  `json:"size"`
	Content  string `json:"content,omitempty"`  // base64, files only
	Encoding string `json:"encoding,omitempty"` // "base64"
}

// getContents handles GET /repos/{owner}/{repo}/contents/{path...} — a basic
// GitHub-compatible read of the local checkout (directory listing or file
// content). The clone is Runway's working copy, so this doubles as a "what is
// actually checked out" inspector.
func (s *Server) getContents(w http.ResponseWriter, r *http.Request) {
	repo := s.repoFromPath(w, r)
	if repo == nil {
		return
	}
	if repo.ClonePath == nil || *repo.ClonePath == "" {
		writeError(w, http.StatusConflict, "repository has not been cloned yet — dispatch a run or wait for the poller")
		return
	}
	root, err := filepath.Abs(*repo.ClonePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	rel := strings.Trim(r.PathValue("path"), "/")
	// Path traversal guard: resolve and require the result stays inside the
	// clone; the .git directory is never exposed.
	target := filepath.Join(root, filepath.FromSlash(rel))
	if target != root && !strings.HasPrefix(target, root+string(filepath.Separator)) {
		writeError(w, http.StatusUnprocessableEntity, "invalid path")
		return
	}
	if rel == ".git" || strings.HasPrefix(rel, ".git/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	info, err := os.Lstat(target)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	if info.IsDir() {
		entries, err := os.ReadDir(target)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := []contentEntry{}
		for _, e := range entries {
			if e.Name() == ".git" {
				continue
			}
			t := "file"
			var size int64
			if e.IsDir() {
				t = "dir"
			} else if fi, err := e.Info(); err == nil {
				size = fi.Size()
				if fi.Mode()&os.ModeSymlink != 0 {
					t = "symlink"
				}
			}
			p := e.Name()
			if rel != "" {
				p = rel + "/" + e.Name()
			}
			out = append(out, contentEntry{Name: e.Name(), Path: p, Type: t, Size: size})
		}
		// Directories first, then files, both alphabetical — like GitHub.
		sort.Slice(out, func(i, j int) bool {
			if out[i].Type != out[j].Type {
				return out[i].Type == "dir"
			}
			return out[i].Name < out[j].Name
		})
		writeJSON(w, out)
		return
	}

	if info.Size() > maxBlobSize {
		writeError(w, http.StatusRequestEntityTooLarge, "file exceeds 1 MiB preview limit")
		return
	}
	b, err := os.ReadFile(target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, contentEntry{
		Name:     filepath.Base(target),
		Path:     rel,
		Type:     "file",
		Size:     info.Size(),
		Content:  base64.StdEncoding.EncodeToString(b),
		Encoding: "base64",
	})
}

// getGitStatus handles GET /api/repos/{id}/gitstatus — whether the local
// checkout is in sync with origin (fetches first, best-effort).
func (s *Server) getGitStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid repo id")
		return
	}
	repo, err := dbpkg.GetRepo(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if repo == nil {
		writeError(w, http.StatusNotFound, "repo not found")
		return
	}
	if repo.ClonePath == nil || *repo.ClonePath == "" {
		writeJSON(w, map[string]any{"cloned": false})
		return
	}
	dir := *repo.ClonePath
	branch := repo.DefaultBranch

	gitOut := func(timeout time.Duration, args ...string) string {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}

	// Best-effort fetch so behind/ahead is current; ignore failures (offline
	// origin should not break the status display).
	_ = gitOut(8*time.Second, "fetch", "--quiet", "origin", branch)

	local := gitOut(3*time.Second, "rev-parse", "HEAD")
	remote := gitOut(3*time.Second, "rev-parse", "origin/"+branch)
	behind := gitOut(3*time.Second, "rev-list", "--count", "HEAD..origin/"+branch)
	ahead := gitOut(3*time.Second, "rev-list", "--count", "origin/"+branch+"..HEAD")

	writeJSON(w, map[string]any{
		"cloned":     true,
		"branch":     branch,
		"local_sha":  local,
		"remote_sha": remote,
		"behind":     atoiSafe(behind),
		"ahead":      atoiSafe(ahead),
		"synced":     local != "" && local == remote,
	})
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
