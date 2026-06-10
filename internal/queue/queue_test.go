package queue

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/aedatum/runway/internal/db"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := dbpkg.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func insertRepo(t *testing.T, d *sql.DB) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO repos(name,owner,git_url,default_branch,created_at)
		 VALUES('app','org','https://example.com/org/app.git','main',?)`,
		time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func enqueue(t *testing.T, d *sql.DB, repoID int64, status string) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO queue(repo_id,workflow_file,branch,status,created_at)
		 VALUES(?,?,?,?,?)`,
		repoID, "ci.yml", "main", status, time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestDequeueRespectsConcurrencyLimit(t *testing.T) {
	d := openTestDB(t)
	repoID := insertRepo(t, d)
	for i := 0; i < 5; i++ {
		enqueue(t, d, repoID, "queued")
	}

	e := NewEngine(d, t.TempDir())

	items, err := e.dequeue(2)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("dequeued %d items, want 2", len(items))
	}

	// The two claimed items now count as running — no free slots remain.
	items, err = e.dequeue(2)
	if err != nil {
		t.Fatalf("second dequeue: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("dequeued %d items at capacity, want 0", len(items))
	}

	// Raising the limit frees slots for the remaining queued items.
	items, err = e.dequeue(4)
	if err != nil {
		t.Fatalf("third dequeue: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("dequeued %d items after limit raise, want 2", len(items))
	}
}

func TestDequeueCountsPreexistingRunning(t *testing.T) {
	d := openTestDB(t)
	repoID := insertRepo(t, d)
	enqueue(t, d, repoID, "running") // e.g. claimed before a restart
	enqueue(t, d, repoID, "queued")

	e := NewEngine(d, t.TempDir())
	items, err := e.dequeue(1)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("dequeued %d items, want 0 (running row occupies the slot)", len(items))
	}
}

func TestHealStaleFailsRunningItems(t *testing.T) {
	d := openTestDB(t)
	repoID := insertRepo(t, d)
	id := enqueue(t, d, repoID, "running")

	e := NewEngine(d, t.TempDir())
	e.healStale()

	var status string
	if err := d.QueryRow(`SELECT status FROM queue WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "failed" {
		t.Errorf("stale running item status = %q, want %q", status, "failed")
	}
}

func TestSSHCommand(t *testing.T) {
	// Non-PEM input (e.g. a path, which could inject into the shell string)
	// must be rejected.
	if cmd, _ := sshCommand("/etc/passwd"); cmd != "" {
		t.Errorf("non-PEM deploy key accepted: %q", cmd)
	}
	if cmd, _ := sshCommand("'; rm -rf / #"); cmd != "" {
		t.Errorf("shell metacharacters accepted: %q", cmd)
	}

	cmd, keyPath := sshCommand("-----BEGIN OPENSSH PRIVATE KEY-----\nfake\n-----END OPENSSH PRIVATE KEY-----\n")
	if cmd == "" {
		t.Fatal("valid PEM rejected")
	}
	defer os.Remove(keyPath)

	if !strings.Contains(cmd, "StrictHostKeyChecking=accept-new") {
		t.Errorf("host key checking not accept-new: %q", cmd)
	}
	if strings.Contains(cmd, "StrictHostKeyChecking=no") {
		t.Errorf("host key checking disabled: %q", cmd)
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode = %o, want 600", perm)
	}
}

func TestWorkflowName(t *testing.T) {
	cases := map[string]string{
		"deploy.yml": "deploy",
		"ci.yaml":    "ci",
		"no-ext":     "no-ext",
		"a.yml.yaml": "a.yml",
	}
	for in, want := range cases {
		if got := workflowName(in); got != want {
			t.Errorf("workflowName(%q) = %q, want %q", in, got, want)
		}
	}
}
