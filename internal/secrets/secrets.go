// Package secrets provides encryption-at-rest for repo/environment secrets
// (AES-256-GCM under a master key) plus the X25519 sealed-box decryption used
// by the GitHub-compatible secrets API (clients encrypt against the public key
// from GET .../actions/secrets/public-key, like gh does against github.com).
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

// Cipher encrypts/decrypts secret values with a 32-byte master key.
type Cipher struct {
	key     [32]byte
	boxPub  [32]byte
	boxPriv [32]byte
}

// LoadKey returns a Cipher from the RUNWAY_SECRETS_KEY env var (64 hex chars).
// When unset, a key is generated once and stored at <dataDir>/secrets.key with
// 0600 permissions — protect that file like the database itself.
func LoadKey(envKey, dataDir string) (*Cipher, error) {
	if envKey != "" {
		raw, err := hex.DecodeString(strings.TrimSpace(envKey))
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("RUNWAY_SECRETS_KEY must be 64 hex chars (32 bytes)")
		}
		return newCipher(raw), nil
	}

	path := filepath.Join(dataDir, "secrets.key")
	if b, err := os.ReadFile(path); err == nil {
		raw, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("invalid key file %s", path)
		}
		return newCipher(raw), nil
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(raw)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write key file: %w", err)
	}
	return newCipher(raw), nil
}

func newCipher(raw []byte) *Cipher {
	c := &Cipher{}
	copy(c.key[:], raw)
	// Deterministic X25519 keypair derived from the master key, so the
	// public key survives restarts without extra storage.
	seed := sha256.Sum256(append(raw, []byte("runway-box-v1")...))
	copy(c.boxPriv[:], seed[:])
	// Clamp per RFC 7748.
	c.boxPriv[0] &= 248
	c.boxPriv[31] &= 127
	c.boxPriv[31] |= 64
	pub, _ := curve25519.X25519(c.boxPriv[:], curve25519.Basepoint)
	copy(c.boxPub[:], pub)
	return c
}

// Encrypt returns nonce||ciphertext for value.
func (c *Cipher) Encrypt(value string) ([]byte, error) {
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(value), nil), nil
}

// Decrypt reverses Encrypt.
func (c *Cipher) Decrypt(blob []byte) (string, error) {
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(blob) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	pt, err := gcm.Open(nil, blob[:gcm.NonceSize()], blob[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(pt), nil
}

// PublicKey returns the X25519 public key clients seal secrets against.
func (c *Cipher) PublicKey() [32]byte { return c.boxPub }

// KeyID identifies the current public key (GitHub returns one too).
func (c *Cipher) KeyID() string {
	sum := sha256.Sum256(c.boxPub[:])
	return hex.EncodeToString(sum[:8])
}

// OpenSealedBox decrypts a libsodium crypto_box_seal message (what gh and the
// GitHub API docs produce when setting secrets).
func (c *Cipher) OpenSealedBox(sealed []byte) (string, error) {
	pt, ok := box.OpenAnonymous(nil, sealed, &c.boxPub, &c.boxPriv)
	if !ok {
		return "", fmt.Errorf("sealed box decryption failed (wrong public key?)")
	}
	return string(pt), nil
}

// ── dotenv-style file helpers (act --secret-file / --var-file format) ─────────

// ParseDotenv reads simple KEY=VALUE lines (optional surrounding quotes),
// ignoring blanks and #comments. Used to merge the operator's global
// SECRETS_FILE with per-repo values.
func ParseDotenv(content string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if k != "" {
			out[k] = v
		}
	}
	return out
}

// WriteDotenvFile writes vals as a 0600 temp file act can consume and returns
// its path. Values are double-quoted with escaping so multi-line secrets work.
func WriteDotenvFile(prefix string, vals map[string]string) (string, error) {
	f, err := os.CreateTemp("", prefix)
	if err != nil {
		return "", err
	}
	_ = f.Chmod(0o600)
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := vals[k]
		v = strings.ReplaceAll(v, `\`, `\\`)
		v = strings.ReplaceAll(v, `"`, `\"`)
		v = strings.ReplaceAll(v, "\n", `\n`)
		if _, err := fmt.Fprintf(f, "%s=\"%s\"\n", k, v); err != nil {
			f.Close()
			os.Remove(f.Name())
			return "", err
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
