package db

import (
	"database/sql"
	"time"
)

const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// User is a Runway user account.
type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	Role         string `json:"role"` // admin | viewer
	PasswordHash string `json:"-"`
	MustChangePW bool   `json:"must_change_pw"`
	CreatedAt    int64  `json:"created_at"`
}

// Session is an authenticated browser session.
type Session struct {
	TokenHash string
	UserID    int64
	ExpiresAt int64
}

// APIKey is a long-lived programmatic access token.
type APIKey struct {
	ID         int64  `json:"id"`
	KeyHash    string `json:"-"`
	Name       string `json:"name"`
	CreatedBy  int64  `json:"created_by"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt *int64 `json:"last_used_at,omitempty"`
}

// CreateUser inserts a new user and returns the assigned ID.
func CreateUser(db *sql.DB, u User) (int64, error) {
	if u.CreatedAt == 0 {
		u.CreatedAt = time.Now().Unix()
	}
	res, err := db.Exec(
		`INSERT INTO users(username,role,password_hash,must_change_pw,created_at)
		 VALUES(?,?,?,?,?)`,
		u.Username, u.Role, u.PasswordHash, boolInt(u.MustChangePW), u.CreatedAt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetUserByUsername returns the user with the given username, or nil if not found.
func GetUserByUsername(db *sql.DB, username string) (*User, error) {
	var u User
	var mustChange int
	err := db.QueryRow(
		`SELECT id,username,role,password_hash,must_change_pw,created_at
		 FROM users WHERE username=?`, username,
	).Scan(&u.ID, &u.Username, &u.Role, &u.PasswordHash, &mustChange, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.MustChangePW = mustChange != 0
	return &u, nil
}

// GetUserByID returns the user with the given ID, or nil.
func GetUserByID(db *sql.DB, id int64) (*User, error) {
	var u User
	var mustChange int
	err := db.QueryRow(
		`SELECT id,username,role,password_hash,must_change_pw,created_at
		 FROM users WHERE id=?`, id,
	).Scan(&u.ID, &u.Username, &u.Role, &u.PasswordHash, &mustChange, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.MustChangePW = mustChange != 0
	return &u, nil
}

// ListUsers returns all users ordered by username.
func ListUsers(db *sql.DB) ([]User, error) {
	rows, err := db.Query(
		`SELECT id,username,role,password_hash,must_change_pw,created_at
		 FROM users ORDER BY username`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var mustChange int
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.PasswordHash, &mustChange, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.MustChangePW = mustChange != 0
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdatePassword sets a new password hash and clears must_change_pw.
func UpdatePassword(db *sql.DB, userID int64, passwordHash string) error {
	_, err := db.Exec(
		`UPDATE users SET password_hash=?, must_change_pw=0 WHERE id=?`,
		passwordHash, userID,
	)
	return err
}

// UpdateUserRole changes a user's role.
func UpdateUserRole(db *sql.DB, userID int64, role string) error {
	_, err := db.Exec(`UPDATE users SET role=? WHERE id=?`, role, userID)
	return err
}

// DeleteUser removes a user and all their sessions.
func DeleteUser(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM users WHERE id=?`, id)
	return err
}

// ── Sessions ──────────────────────────────────────────────────────────────────

// CreateSession inserts a new session record.
func CreateSession(db *sql.DB, s Session) error {
	_, err := db.Exec(
		`INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,?)`,
		s.TokenHash, s.UserID, s.ExpiresAt,
	)
	return err
}

// GetSessionUser returns the user for a valid (non-expired) session token hash.
func GetSessionUser(db *sql.DB, tokenHash string) (*User, error) {
	var userID int64
	err := db.QueryRow(
		`SELECT user_id FROM sessions WHERE token_hash=? AND expires_at>?`,
		tokenHash, time.Now().Unix(),
	).Scan(&userID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return GetUserByID(db, userID)
}

// DeleteSession removes a session (logout).
func DeleteSession(db *sql.DB, tokenHash string) error {
	_, err := db.Exec(`DELETE FROM sessions WHERE token_hash=?`, tokenHash)
	return err
}

// PruneExpiredSessions removes sessions past their expiry.
func PruneExpiredSessions(db *sql.DB) error {
	_, err := db.Exec(`DELETE FROM sessions WHERE expires_at<?`, time.Now().Unix())
	return err
}

// ── API Keys ──────────────────────────────────────────────────────────────────

// CreateAPIKey inserts a new API key record.
func CreateAPIKey(db *sql.DB, k APIKey) (int64, error) {
	if k.CreatedAt == 0 {
		k.CreatedAt = time.Now().Unix()
	}
	res, err := db.Exec(
		`INSERT INTO api_keys(key_hash,name,created_by,created_at) VALUES(?,?,?,?)`,
		k.KeyHash, k.Name, k.CreatedBy, k.CreatedAt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetAPIKeyUser returns the user associated with a valid API key hash.
func GetAPIKeyUser(db *sql.DB, keyHash string) (*User, error) {
	var userID int64
	err := db.QueryRow(
		`SELECT created_by FROM api_keys WHERE key_hash=?`, keyHash,
	).Scan(&userID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Update last_used_at asynchronously — best effort.
	go func() {
		now := time.Now().Unix()
		db.Exec(`UPDATE api_keys SET last_used_at=? WHERE key_hash=?`, now, keyHash) //nolint:errcheck
	}()
	return GetUserByID(db, userID)
}

// ListAPIKeys returns all API keys for a user (hashes are not included).
func ListAPIKeys(db *sql.DB, userID int64) ([]APIKey, error) {
	rows, err := db.Query(
		`SELECT id,name,created_by,created_at,last_used_at
		 FROM api_keys WHERE created_by=? ORDER BY created_at DESC`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.Name, &k.CreatedBy, &k.CreatedAt, &k.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteAPIKey removes an API key by ID, scoped to the owning user.
func DeleteAPIKey(db *sql.DB, id, userID int64) error {
	_, err := db.Exec(`DELETE FROM api_keys WHERE id=? AND created_by=?`, id, userID)
	return err
}

// ── helpers ───────────────────────────────────────────────────────────────────

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
