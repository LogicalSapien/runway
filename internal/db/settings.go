package db

import (
	"database/sql"
	"strconv"
)

// GetSetting returns the value for key, or defaultVal if not found.
func GetSetting(db *sql.DB, key, defaultVal string) (string, error) {
	var val string
	err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&val)
	if err == sql.ErrNoRows {
		return defaultVal, nil
	}
	if err != nil {
		return defaultVal, err
	}
	return val, nil
}

// SetSetting inserts or updates key with value.
func SetSetting(db *sql.DB, key, value string) error {
	_, err := db.Exec(
		`INSERT INTO settings(key,value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value,
	)
	return err
}

// GetConcurrency returns the maximum number of concurrent workflow runs.
// Defaults to 1 if the setting is missing or unparseable.
func GetConcurrency(db *sql.DB) int {
	val, err := GetSetting(db, "concurrency", "1")
	if err != nil {
		return 1
	}
	n, err := strconv.Atoi(val)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// GetAPITokenHash returns the stored bcrypt hash of the API token.
// Returns "" if no token has been configured (open access).
func GetAPITokenHash(db *sql.DB) string {
	val, _ := GetSetting(db, "api_token_hash", "")
	return val
}

// SeedSetting writes key=value only if the key does not already exist.
// Used at boot to set defaults without overwriting user changes.
func SeedSetting(db *sql.DB, key, value string) error {
	_, err := db.Exec(
		`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO NOTHING`,
		key, value,
	)
	return err
}
