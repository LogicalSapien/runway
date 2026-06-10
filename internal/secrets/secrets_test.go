package secrets

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/nacl/box"
)

func testCipher(t *testing.T) *Cipher {
	t.Helper()
	c, err := LoadKey("", t.TempDir())
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	return c
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c := testCipher(t)
	for _, val := range []string{"hunter2", "", "multi\nline\nsecret", strings.Repeat("x", 10000)} {
		enc, err := c.Encrypt(val)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		if strings.Contains(string(enc), val) && val != "" {
			t.Error("ciphertext contains plaintext")
		}
		got, err := c.Decrypt(enc)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if got != val {
			t.Errorf("roundtrip mismatch: %q != %q", got, val)
		}
	}
}

func TestKeyfilePersistsAcrossLoads(t *testing.T) {
	dir := t.TempDir()
	c1, err := LoadKey("", dir)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := c1.Encrypt("value")

	c2, err := LoadKey("", dir) // second boot — must reuse the same keyfile
	if err != nil {
		t.Fatal(err)
	}
	got, err := c2.Decrypt(enc)
	if err != nil || got != "value" {
		t.Fatalf("decrypt after reload: %q, %v", got, err)
	}

	info, _ := os.Stat(filepath.Join(dir, "secrets.key"))
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("keyfile mode = %o, want 600", perm)
	}
}

func TestWrongKeyFailsClosed(t *testing.T) {
	c1 := testCipher(t)
	c2 := testCipher(t)
	enc, _ := c1.Encrypt("value")
	if _, err := c2.Decrypt(enc); err == nil {
		t.Error("decrypt with wrong key succeeded")
	}
}

func TestSealedBox(t *testing.T) {
	c := testCipher(t)
	pub := c.PublicKey()
	sealed, err := box.SealAnonymous(nil, []byte("gh-style secret"), &pub, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.OpenSealedBox(sealed)
	if err != nil {
		t.Fatalf("open sealed box: %v", err)
	}
	if got != "gh-style secret" {
		t.Errorf("got %q", got)
	}
	if _, err := c.OpenSealedBox([]byte("garbage")); err == nil {
		t.Error("garbage sealed box accepted")
	}
}

func TestDotenvRoundTrip(t *testing.T) {
	vals := map[string]string{
		"SIMPLE":    "value",
		"WITH_EQ":   "a=b=c",
		"QUOTED":    `he said "hi"`,
		"MULTILINE": "line1\nline2",
	}
	path, err := WriteDotenvFile("test-*", vals)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}

	b, _ := os.ReadFile(path)
	parsed := ParseDotenv(string(b))
	if parsed["SIMPLE"] != "value" || parsed["WITH_EQ"] != "a=b=c" {
		t.Errorf("parse mismatch: %v", parsed)
	}
}

func TestParseDotenvIgnoresJunk(t *testing.T) {
	m := ParseDotenv("# comment\n\nKEY=val\nNOEQUALS\n  SPACED = padded \n")
	if len(m) != 2 || m["KEY"] != "val" || m["SPACED"] != "padded" {
		t.Errorf("got %v", m)
	}
}
