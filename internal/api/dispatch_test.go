package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/aedatum/runway/internal/db"
	"github.com/aedatum/runway/internal/secrets"
)

func newTestServer(t *testing.T) (*Server, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	d, err := dbpkg.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	cipher, err := secrets.LoadKey("", dir)
	if err != nil {
		t.Fatalf("load test cipher: %v", err)
	}
	return NewServer(d, cipher), d
}

func registerRepo(t *testing.T, d *sql.DB) {
	t.Helper()
	_, err := d.Exec(
		`INSERT INTO repos(name,owner,git_url,default_branch,created_at)
		 VALUES('app','org','https://example.com/org/app.git','main',?)`,
		time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
}

func postDispatch(s *Server, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestDispatchRejectsPathTraversal(t *testing.T) {
	s, d := newTestServer(t)
	registerRepo(t, d)

	bad := []string{
		"/repos/org/app/actions/workflows/..%2F..%2Fetc%2Fpasswd/dispatches",
		"/repos/org/app/actions/workflows/ci.json/dispatches",
		"/repos/org/app/actions/workflows/.hidden.yml/dispatches",
	}
	for _, path := range bad {
		rec := postDispatch(s, path, `{"ref":"main"}`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("POST %s = %d, want 422", path, rec.Code)
		}
	}

	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM queue`).Scan(&n)
	if n != 0 {
		t.Errorf("queue has %d items after rejected dispatches, want 0", n)
	}
}

func TestDispatchRejectsBadRef(t *testing.T) {
	s, d := newTestServer(t)
	registerRepo(t, d)

	for _, ref := range []string{"", "-rf", "a..b", "x;y", "br anch"} {
		rec := postDispatch(s,
			"/repos/org/app/actions/workflows/ci.yml/dispatches",
			`{"ref":"`+ref+`"}`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("ref %q: status = %d, want 422", ref, rec.Code)
		}
	}
}

func TestDispatchUnknownRepo(t *testing.T) {
	s, _ := newTestServer(t)
	rec := postDispatch(s,
		"/repos/nobody/nothing/actions/workflows/ci.yml/dispatches",
		`{"ref":"main"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestDispatchEnqueues(t *testing.T) {
	s, d := newTestServer(t)
	registerRepo(t, d)

	rec := postDispatch(s,
		"/repos/org/app/actions/workflows/ci.yml/dispatches",
		`{"ref":"main","inputs":{"env":"staging"}}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("X-Runway-Queue-URL"); loc == "" {
		t.Error("missing X-Runway-Queue-URL header")
	}

	var file, branch, inputs string
	err := d.QueryRow(
		`SELECT workflow_file, branch, COALESCE(inputs,'') FROM queue`,
	).Scan(&file, &branch, &inputs)
	if err != nil {
		t.Fatalf("queue row: %v", err)
	}
	if file != "ci.yml" || branch != "main" {
		t.Errorf("queue row = (%q,%q), want (ci.yml, main)", file, branch)
	}
	if !strings.Contains(inputs, `"env":"staging"`) {
		t.Errorf("inputs = %q, want to contain env:staging", inputs)
	}
}
