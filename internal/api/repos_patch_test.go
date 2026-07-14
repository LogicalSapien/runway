package api

import (
	"encoding/json"
	"net/http"
	"testing"

	dbpkg "github.com/LogicalSapien/runway/internal/db"
)

func TestPatchRepo(t *testing.T) {
	s, d := newTestServer(t)
	registerRepo(t, d)

	repo, err := dbpkg.GetRepoByOwnerName(d, "org", "app")
	if err != nil || repo == nil {
		t.Fatalf("fixture repo: %v", err)
	}
	// Simulate an existing checkout so we can assert it is dropped on URL change.
	cp := "/tmp/clones/app"
	repo.ClonePath = &cp
	if err := dbpkg.UpdateRepo(d, *repo); err != nil {
		t.Fatal(err)
	}

	// Change the git URL — clone_path must be cleared, key never echoed.
	rec := do(s, adminRequest(t, d, "PATCH", "/api/repos/1",
		`{"git_url":"git@github.com:org/app.git"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: got %d body=%s", rec.Code, rec.Body.String())
	}
	var got dbpkg.Repo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.GitURL != "git@github.com:org/app.git" {
		t.Fatalf("git_url not updated: %q", got.GitURL)
	}
	if got.ClonePath != nil {
		t.Fatalf("clone_path should be cleared on git_url change, got %q", *got.ClonePath)
	}
	if got.DeployKey != nil {
		t.Fatal("deploy_key must never be echoed")
	}

	// Unknown id → 404, bad id → 400.
	if rec := do(s, adminRequest(t, d, "PATCH", "/api/repos/999", `{}`)); rec.Code != http.StatusNotFound {
		t.Fatalf("missing repo: got %d", rec.Code)
	}
	if rec := do(s, adminRequest(t, d, "PATCH", "/api/repos/zzz", `{}`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: got %d", rec.Code)
	}
}
