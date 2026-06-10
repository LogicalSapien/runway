// Package auth handles password hashing, token generation, and the
// first-boot bootstrap that seeds the admin user from config.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"

	"golang.org/x/crypto/bcrypt"

	dbpkg "github.com/aedatum/runway/internal/db"
)

const (
	// DefaultAdminUsername is the built-in admin account name.
	DefaultAdminUsername = "admin"
	// DefaultAdminPassword is the factory default — users are prompted to change it.
	DefaultAdminPassword = "runway"

	bcryptCost      = 12
	sessionTokenLen = 32 // bytes → 64 hex chars
	apiKeyLen       = 32 // bytes → 64 hex chars
)

// Bootstrap ensures an admin user exists in the DB. It is called once at
// startup. Logic:
//   - If an admin user already exists → do nothing.
//   - Otherwise → create one with the password from adminPassword (falls back
//     to DefaultAdminPassword), setting must_change_pw=true.
func Bootstrap(db *sql.DB, adminPassword string) error {
	existing, err := dbpkg.GetUserByUsername(db, DefaultAdminUsername)
	if err != nil {
		return fmt.Errorf("auth bootstrap: %w", err)
	}
	if existing != nil {
		return nil // already seeded
	}

	pw := adminPassword
	if pw == "" {
		pw = DefaultAdminPassword
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return fmt.Errorf("auth bootstrap: hash password: %w", err)
	}

	_, err = dbpkg.CreateUser(db, dbpkg.User{
		Username:     DefaultAdminUsername,
		Role:         dbpkg.RoleAdmin,
		PasswordHash: string(hash),
		MustChangePW: true,
	})
	if err != nil {
		return fmt.Errorf("auth bootstrap: create admin: %w", err)
	}
	log.Printf("auth: seeded admin user (username=%q) — please change the default password", DefaultAdminUsername)
	return nil
}

// CheckPassword returns true when the plaintext password matches the bcrypt hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// HashPassword returns a bcrypt hash of password.
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	return string(b), err
}

// NewSessionToken generates a cryptographically random session token and
// returns (rawToken, tokenHash). Store only the hash in the DB; send the
// raw token to the browser as a cookie value.
func NewSessionToken() (raw, hash string, err error) {
	buf := make([]byte, sessionTokenLen)
	if _, err = rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("session token: %w", err)
	}
	raw = hex.EncodeToString(buf)
	hash = SHA256Hex(raw)
	return raw, hash, nil
}

// NewAPIKey generates a cryptographically random API key and returns
// (rawKey, keyHash). Store only the hash in the DB; show the raw key to
// the user exactly once.
func NewAPIKey() (raw, hash string, err error) {
	buf := make([]byte, apiKeyLen)
	if _, err = rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("api key: %w", err)
	}
	raw = "rwy_" + hex.EncodeToString(buf)
	hash = SHA256Hex(raw)
	return raw, hash, nil
}

// SHA256Hex returns the lowercase hex SHA-256 digest of s.
func SHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
