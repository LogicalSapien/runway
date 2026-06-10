package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/nacl/box"

	dbpkg "github.com/LogicalSapien/runway/internal/db"
	"github.com/LogicalSapien/runway/internal/middleware"
)

// adminRequest builds a request whose context carries an admin user (the tests
// bypass the auth middleware, but RequireAdmin still checks the context).
func adminRequest(t *testing.T, d *sql.DB, method, path, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	u, err := dbpkg.GetUserByUsername(d, "admin-test")
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		uid, err := dbpkg.CreateUser(d, dbpkg.User{Username: "admin-test", Role: dbpkg.RoleAdmin, PasswordHash: "x"})
		if err != nil {
			t.Fatalf("create admin: %v", err)
		}
		u, _ = dbpkg.GetUserByID(d, uid)
	}
	return req.WithContext(middleware.WithUser(req.Context(), u))
}

func do(s *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestSecretsLifecycle(t *testing.T) {
	s, d := newTestServer(t)
	registerRepo(t, d)

	// Create (plaintext extension)
	rec := do(s, adminRequest(t, d, "PUT", "/repos/org/app/actions/secrets/API_TOKEN", `{"value":"s3cret"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("put = %d (%s), want 201", rec.Code, rec.Body)
	}
	// Update → 204
	rec = do(s, adminRequest(t, d, "PUT", "/repos/org/app/actions/secrets/API_TOKEN", `{"value":"s3cret2"}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("update = %d, want 204", rec.Code)
	}

	// List returns names only, never values
	rec = do(s, httptest.NewRequest("GET", "/repos/org/app/actions/secrets", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "API_TOKEN") {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "s3cret") {
		t.Error("secret value leaked in list response")
	}

	// Stored encrypted
	var blob []byte
	_ = d.QueryRow(`SELECT value_enc FROM repo_secrets WHERE name='API_TOKEN'`).Scan(&blob)
	if strings.Contains(string(blob), "s3cret") {
		t.Error("secret stored in plaintext")
	}

	// Bad names rejected
	rec = do(s, adminRequest(t, d, "PUT", "/repos/org/app/actions/secrets/9BAD;NAME", `{"value":"x"}`))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad name = %d, want 422", rec.Code)
	}

	// Delete
	rec = do(s, adminRequest(t, d, "DELETE", "/repos/org/app/actions/secrets/API_TOKEN", ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", rec.Code)
	}
	rec = do(s, adminRequest(t, d, "DELETE", "/repos/org/app/actions/secrets/API_TOKEN", ""))
	if rec.Code != http.StatusNotFound {
		t.Errorf("double delete = %d, want 404", rec.Code)
	}
}

func TestSecretsSealedBoxFlow(t *testing.T) {
	s, d := newTestServer(t)
	registerRepo(t, d)

	// Fetch the public key like gh does
	rec := do(s, httptest.NewRequest("GET", "/repos/org/app/actions/secrets/public-key", nil))
	if rec.Code != 200 {
		t.Fatalf("public-key = %d", rec.Code)
	}
	var pk struct{ Key, KeyID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &struct {
		Key   *string `json:"key"`
		KeyID *string `json:"key_id"`
	}{&pk.Key, &pk.KeyID})

	pubRaw, err := base64.StdEncoding.DecodeString(pk.Key)
	if err != nil || len(pubRaw) != 32 {
		t.Fatalf("bad public key: %v", err)
	}
	var pub [32]byte
	copy(pub[:], pubRaw)
	sealed, err := box.SealAnonymous(nil, []byte("sealed-value"), &pub, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{
		"encrypted_value": base64.StdEncoding.EncodeToString(sealed),
		"key_id":          pk.KeyID,
	})
	rec = do(s, adminRequest(t, d, "PUT", "/repos/org/app/actions/secrets/SEALED", string(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("sealed put = %d (%s)", rec.Code, rec.Body)
	}

	// Verify the stored value decrypts to the original
	var blob []byte
	_ = d.QueryRow(`SELECT value_enc FROM repo_secrets WHERE name='SEALED'`).Scan(&blob)
	got, err := s.cipher.Decrypt(blob)
	if err != nil || got != "sealed-value" {
		t.Errorf("stored sealed secret = %q, %v", got, err)
	}
}

func TestEnvironmentScopedSecretsAndVariables(t *testing.T) {
	s, d := newTestServer(t)
	registerRepo(t, d)

	// Env must exist before scoping to it
	rec := do(s, adminRequest(t, d, "PUT", "/repos/org/app/environments/production/secrets/DEPLOY_KEY", `{"value":"x"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("secret in missing env = %d, want 404", rec.Code)
	}

	rec = do(s, adminRequest(t, d, "PUT", "/repos/org/app/environments/production", ""))
	if rec.Code != 200 {
		t.Fatalf("create env = %d", rec.Code)
	}
	rec = do(s, adminRequest(t, d, "PUT", "/repos/org/app/environments/production/secrets/DEPLOY_KEY", `{"value":"x"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("env secret = %d (%s)", rec.Code, rec.Body)
	}

	// Variables CRUD
	rec = do(s, adminRequest(t, d, "POST", "/repos/org/app/actions/variables", `{"name":"REGION","value":"eu-west-2"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create var = %d (%s)", rec.Code, rec.Body)
	}
	rec = do(s, httptest.NewRequest("GET", "/repos/org/app/actions/variables/REGION", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "eu-west-2") {
		t.Fatalf("get var = %d %s", rec.Code, rec.Body)
	}
	rec = do(s, adminRequest(t, d, "PATCH", "/repos/org/app/actions/variables/REGION", `{"value":"us-east-1"}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("patch var = %d", rec.Code)
	}

	// Deleting the environment removes its scoped values
	rec = do(s, adminRequest(t, d, "DELETE", "/repos/org/app/environments/production", ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete env = %d", rec.Code)
	}
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM repo_secrets WHERE environment='production'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d orphaned env secrets after env delete", n)
	}
}
