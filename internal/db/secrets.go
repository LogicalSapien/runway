package db

import (
	"database/sql"
	"time"
)

// Secret metadata (the value is stored encrypted and never listed back).
type Secret struct {
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// Variable is a non-secret configuration value (GitHub: ${{ vars.NAME }}).
type Variable struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// environment="" means repo-level (the "actions" scope in GitHub terms).

func UpsertSecret(db *sql.DB, repoID int64, environment, name string, valueEnc []byte) error {
	now := time.Now().Unix()
	_, err := db.Exec(`
		INSERT INTO repo_secrets(repo_id, environment, name, value_enc, created_at, updated_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(repo_id, environment, name)
		DO UPDATE SET value_enc=excluded.value_enc, updated_at=excluded.updated_at`,
		repoID, environment, name, valueEnc, now, now)
	return err
}

func ListSecrets(db *sql.DB, repoID int64, environment string) ([]Secret, error) {
	rows, err := db.Query(`
		SELECT name, created_at, updated_at FROM repo_secrets
		WHERE repo_id=? AND environment=? ORDER BY name`, repoID, environment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Secret{}
	for rows.Next() {
		var s Secret
		if err := rows.Scan(&s.Name, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func GetSecret(db *sql.DB, repoID int64, environment, name string) (*Secret, error) {
	var s Secret
	err := db.QueryRow(`
		SELECT name, created_at, updated_at FROM repo_secrets
		WHERE repo_id=? AND environment=? AND name=?`, repoID, environment, name).
		Scan(&s.Name, &s.CreatedAt, &s.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func DeleteSecret(db *sql.DB, repoID int64, environment, name string) (bool, error) {
	res, err := db.Exec(`DELETE FROM repo_secrets WHERE repo_id=? AND environment=? AND name=?`,
		repoID, environment, name)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SecretValuesForRun returns name→encrypted value for a repo, with environment
// scoped values overriding repo-level ones of the same name.
func SecretValuesForRun(db *sql.DB, repoID int64, environments []string) (map[string][]byte, error) {
	out := map[string][]byte{}
	scopes := append([]string{""}, environments...)
	for _, env := range scopes {
		rows, err := db.Query(`SELECT name, value_enc FROM repo_secrets WHERE repo_id=? AND environment=?`,
			repoID, env)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name string
			var enc []byte
			if err := rows.Scan(&name, &enc); err != nil {
				rows.Close()
				return nil, err
			}
			out[name] = enc
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ── variables ────────────────────────────────────────────────────────────────

func UpsertVariable(db *sql.DB, repoID int64, environment, name, value string) error {
	now := time.Now().Unix()
	_, err := db.Exec(`
		INSERT INTO repo_variables(repo_id, environment, name, value, created_at, updated_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(repo_id, environment, name)
		DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		repoID, environment, name, value, now, now)
	return err
}

func ListVariables(db *sql.DB, repoID int64, environment string) ([]Variable, error) {
	rows, err := db.Query(`
		SELECT name, value, created_at, updated_at FROM repo_variables
		WHERE repo_id=? AND environment=? ORDER BY name`, repoID, environment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Variable{}
	for rows.Next() {
		var v Variable
		if err := rows.Scan(&v.Name, &v.Value, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func GetVariable(db *sql.DB, repoID int64, environment, name string) (*Variable, error) {
	var v Variable
	err := db.QueryRow(`
		SELECT name, value, created_at, updated_at FROM repo_variables
		WHERE repo_id=? AND environment=? AND name=?`, repoID, environment, name).
		Scan(&v.Name, &v.Value, &v.CreatedAt, &v.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func DeleteVariable(db *sql.DB, repoID int64, environment, name string) (bool, error) {
	res, err := db.Exec(`DELETE FROM repo_variables WHERE repo_id=? AND environment=? AND name=?`,
		repoID, environment, name)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// VariableValuesForRun mirrors SecretValuesForRun for plaintext variables.
func VariableValuesForRun(db *sql.DB, repoID int64, environments []string) (map[string]string, error) {
	out := map[string]string{}
	scopes := append([]string{""}, environments...)
	for _, env := range scopes {
		rows, err := db.Query(`SELECT name, value FROM repo_variables WHERE repo_id=? AND environment=?`,
			repoID, env)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name, value string
			if err := rows.Scan(&name, &value); err != nil {
				rows.Close()
				return nil, err
			}
			out[name] = value
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ── environments ─────────────────────────────────────────────────────────────

func UpsertEnvironment(db *sql.DB, repoID int64, name string) error {
	_, err := db.Exec(`
		INSERT INTO environments(repo_id, name, created_at) VALUES(?,?,?)
		ON CONFLICT(repo_id, name) DO NOTHING`,
		repoID, name, time.Now().Unix())
	return err
}

func ListEnvironments(db *sql.DB, repoID int64) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM environments WHERE repo_id=? ORDER BY name`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteEnvironment removes an environment and its scoped secrets/variables.
func DeleteEnvironment(db *sql.DB, repoID int64, name string) (bool, error) {
	res, err := db.Exec(`DELETE FROM environments WHERE repo_id=? AND name=?`, repoID, name)
	if err != nil {
		return false, err
	}
	_, _ = db.Exec(`DELETE FROM repo_secrets   WHERE repo_id=? AND environment=?`, repoID, name) //nolint:errcheck
	_, _ = db.Exec(`DELETE FROM repo_variables WHERE repo_id=? AND environment=?`, repoID, name) //nolint:errcheck
	n, _ := res.RowsAffected()
	return n > 0, nil
}
