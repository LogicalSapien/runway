package auth

import (
	"database/sql"
	"path/filepath"
	"testing"

	dbpkg "github.com/LogicalSapien/runway/internal/db"
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

func TestBootstrapSeedsAdminOnce(t *testing.T) {
	d := openTestDB(t)

	if err := Bootstrap(d, "secret-pw"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	u, err := dbpkg.GetUserByUsername(d, DefaultAdminUsername)
	if err != nil {
		t.Fatalf("get admin: %v", err)
	}
	if u == nil {
		t.Fatal("admin user not created")
	}
	if u.Role != dbpkg.RoleAdmin {
		t.Errorf("admin role = %q, want %q", u.Role, dbpkg.RoleAdmin)
	}
	if !u.MustChangePW {
		t.Error("admin must_change_pw = false, want true")
	}
	if !CheckPassword(u.PasswordHash, "secret-pw") {
		t.Error("seeded password does not verify")
	}
	if CheckPassword(u.PasswordHash, "wrong") {
		t.Error("wrong password verified")
	}

	// Second bootstrap must not overwrite the existing admin.
	if err := Bootstrap(d, "different-pw"); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	u2, _ := dbpkg.GetUserByUsername(d, DefaultAdminUsername)
	if !CheckPassword(u2.PasswordHash, "secret-pw") {
		t.Error("second bootstrap overwrote the admin password")
	}
}

func TestBootstrapDefaultPassword(t *testing.T) {
	d := openTestDB(t)
	if err := Bootstrap(d, ""); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	u, _ := dbpkg.GetUserByUsername(d, DefaultAdminUsername)
	if !CheckPassword(u.PasswordHash, DefaultAdminPassword) {
		t.Error("empty config password should fall back to the default")
	}
}

func TestHashPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hash == "hunter2" {
		t.Fatal("hash equals plaintext")
	}
	if !CheckPassword(hash, "hunter2") {
		t.Error("correct password rejected")
	}
	if CheckPassword(hash, "hunter3") {
		t.Error("wrong password accepted")
	}
}

func TestNewSessionToken(t *testing.T) {
	raw1, hash1, err := NewSessionToken()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	raw2, _, _ := NewSessionToken()
	if raw1 == raw2 {
		t.Error("two tokens are identical")
	}
	if len(raw1) != sessionTokenLen*2 {
		t.Errorf("token length = %d, want %d", len(raw1), sessionTokenLen*2)
	}
	if hash1 != SHA256Hex(raw1) {
		t.Error("hash does not match SHA256(raw)")
	}
	if raw1 == hash1 {
		t.Error("raw token equals its hash")
	}
}

func TestNewAPIKey(t *testing.T) {
	raw, hash, err := NewAPIKey()
	if err != nil {
		t.Fatalf("api key: %v", err)
	}
	if len(raw) < 4 || raw[:4] != "rwy_" {
		t.Errorf("api key = %q, want rwy_ prefix", raw)
	}
	if hash != SHA256Hex(raw) {
		t.Error("hash does not match SHA256(raw)")
	}
}
