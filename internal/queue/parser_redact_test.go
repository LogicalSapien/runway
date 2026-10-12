package queue

import (
	"strings"
	"testing"
	"time"

	dbpkg "github.com/LogicalSapien/runway/internal/db"
	"github.com/LogicalSapien/runway/internal/redact"
	"github.com/LogicalSapien/runway/internal/secrets"
)

// TestParserStoresRedactedLines feeds act-style output through the parser and
// checks that no secret reaches the logs table.
func TestParserStoresRedactedLines(t *testing.T) {
	d := openTestDB(t)
	repoID := insertRepo(t, d)
	res, err := d.Exec(`INSERT INTO runs(repo_id,repo,workflow,workflow_file,branch,status,trigger,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, repoID, "app", "Deploy", "deploy.yml", "main", "running", "api", time.Now().Unix())
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	runID, _ := res.LastInsertId()

	known := "known-repo-secret-value"
	body := strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo", 2)
	p := NewParser(d, runID)
	p.redactor = redact.New([]string{known})
	for _, l := range []string{
		"[Deploy/deploy] ⭐ Run Main Fetch secrets",
		"[Deploy/deploy]   ⚙  ::set-env:: COOKIE_SECRET=abcdefghijklmnop",
		"[Deploy/deploy]   ⚙  ::set-env:: KAMAL_SSH_PRIVATE_KEY=-----BEGIN OPENSSH " + "PRIVATE KEY-----",
		body,
		"-----END OPENSSH " + "PRIVATE KEY-----",
		"[Deploy/deploy]   | uses " + known,
		"[Deploy/deploy]   | DATABASE_URL=postgresql://u:pa55word@db:5432/app",
		"[Deploy/deploy]   ✅  Success - Main Fetch secrets [1s]",
	} {
		p.Feed(l)
	}
	p.Flush()

	rows, err := d.Query(`SELECT text FROM logs WHERE run_id=?`, runID)
	if err != nil {
		t.Fatalf("query logs: %v", err)
	}
	defer rows.Close()
	var all []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		all = append(all, s)
	}
	joined := strings.Join(all, "\n")
	for _, secret := range []string{"abcdefghijklmnop", body, known, "pa55word"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("secret %q stored in logs:\n%s", secret, joined)
		}
	}
	if len(all) == 0 {
		t.Fatal("expected log lines to be stored")
	}
}

// TestBuildRunFilesReturnsRawSecretValues checks the redactor gets each secret
// exactly as stored (quotes, backslashes, newlines intact), not a re-parse of
// the dotenv-encoded secrets file, which could differ from what act injects.
func TestBuildRunFilesReturnsRawSecretValues(t *testing.T) {
	d := openTestDB(t)
	repoID := insertRepo(t, d)
	cipher, err := secrets.LoadKey("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw := "line-one \"quoted\" \\back\nline-two-value"
	enc, err := cipher.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.UpsertSecret(d, repoID, "", "MULTI", enc); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(d, t.TempDir(), cipher)
	f, _, known, cleanup := e.buildRunFiles(QueueItem{RepoID: repoID, WorkflowFile: "ci.yml"}, t.TempDir())
	defer cleanup()
	if f == "" {
		t.Fatal("expected a secrets file")
	}
	if len(known) != 1 || known[0] != raw {
		t.Fatalf("known = %q, want [%q]", known, raw)
	}
}
