package api

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func registerClonedRepo(t *testing.T, d *sql.DB) string {
	t.Helper()
	clone := t.TempDir()
	if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(clone, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(clone, "README.md"), []byte("# hello"), 0o644)           //nolint:errcheck
	os.WriteFile(filepath.Join(clone, "src", "main.go"), []byte("package main"), 0o644) //nolint:errcheck
	os.WriteFile(filepath.Join(clone, ".git", "config"), []byte("[core]"), 0o644)       //nolint:errcheck
	os.WriteFile(filepath.Join(filepath.Dir(clone), "outside.txt"), []byte("x"), 0o644) //nolint:errcheck

	_, err := d.Exec(
		`INSERT INTO repos(name,owner,git_url,default_branch,clone_path,created_at)
		 VALUES('app','org','https://example.com/org/app.git','main',?,?)`,
		clone, time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	return clone
}

func TestContentsListingAndFile(t *testing.T) {
	s, d := newTestServer(t)
	registerClonedRepo(t, d)

	// Root listing: dirs first, .git hidden
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/repos/org/app/contents", nil))
	if rec.Code != 200 {
		t.Fatalf("list = %d (%s)", rec.Code, rec.Body)
	}
	var entries []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &entries)
	names := []string{}
	for _, e := range entries {
		names = append(names, e["name"].(string))
	}
	if strings.Join(names, ",") != "src,README.md" {
		t.Errorf("listing = %v, want [src README.md] (dirs first, .git hidden)", names)
	}

	// File content round-trips via base64
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/repos/org/app/contents/src/main.go", nil))
	var f map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &f)
	raw, _ := base64.StdEncoding.DecodeString(f["content"].(string))
	if string(raw) != "package main" {
		t.Errorf("content = %q", raw)
	}
}

func TestContentsBlocksTraversalAndGitDir(t *testing.T) {
	s, d := newTestServer(t)
	registerClonedRepo(t, d)

	for path, want := range map[string]int{
		"/repos/org/app/contents/..%2Foutside.txt": http.StatusUnprocessableEntity,
		"/repos/org/app/contents/.git/config":      http.StatusNotFound,
		"/repos/org/app/contents/nope.txt":         http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want && rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want %d-ish rejection", path, rec.Code, want)
		}
		if strings.Contains(rec.Body.String(), "outside") || strings.Contains(rec.Body.String(), "[core]") {
			t.Errorf("GET %s leaked content: %s", path, rec.Body.String()[:80])
		}
	}
}
