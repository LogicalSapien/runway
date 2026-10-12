package redact

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

// Fake values only — built at run time so no scanner mistakes them for real keys.
var (
	keyBody1 = strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo", 2)
	keyBody2 = strings.Repeat("YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo", 2)
	pemBegin = "-----BEGIN OPENSSH " + "PRIVATE KEY-----"
	pemEnd   = "-----END OPENSSH " + "PRIVATE KEY-----"
)

func feed(r *Redactor, lines ...string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = r.Line(l)
	}
	return out
}

func mustNotContain(t *testing.T, got []string, secrets ...string) {
	t.Helper()
	joined := strings.Join(got, "\n")
	for _, s := range secrets {
		if strings.Contains(joined, s) {
			t.Fatalf("secret %q survived redaction:\n%s", s, joined)
		}
	}
}

func TestKnownValuesMaskedLineByLine(t *testing.T) {
	key := pemBegin + "\n" + keyBody1 + "\n" + keyBody2 + "\n" + pemEnd
	r := New([]string{key, "s3cr3t-value-123", "x"})
	got := feed(r,
		"[deploy/build] | using s3cr3t-value-123 now",
		"[deploy/build] | "+keyBody1,
		"[deploy/build] | "+keyBody2,
		"[deploy/build] | x marks the spot", // shorter than minLen: untouched
	)
	mustNotContain(t, got, "s3cr3t-value-123", keyBody1, keyBody2)
	if got[3] != "[deploy/build] | x marks the spot" {
		t.Fatalf("short value must not be masked, got %q", got[3])
	}
}

func TestAddMaskRegistersValue(t *testing.T) {
	r := New(nil)
	got := feed(r,
		"[j/s]   ⚙  ::add-mask::hunter2-token-abc",
		"[j/s]   | token is hunter2-token-abc",
	)
	mustNotContain(t, got, "hunter2-token-abc")
	if got[0] != "[j/s]   ⚙  ::add-mask::***" {
		t.Fatalf("add-mask line not masked: %q", got[0])
	}
}

func TestSetEnvSecretNameMasked(t *testing.T) {
	r := New(nil)
	fake := strings.Repeat("ab12", 4)
	got := feed(r,
		"[Deploy/Deploy (stg)]   ⚙  ::set-env:: R2_SECRET_ACCESS_KEY="+fake,
		"[Deploy/Deploy (stg)]   ⚙  ::set-env:: DATABASE_URL=postgres://u:p4ss@db:5432/x",
		"[Deploy/Deploy (stg)]   ⚙  ::set-env:: BETTER_AUTH_URL=https://example.test",
		"[Deploy/Deploy (stg)]   ⚙  ::set-output:: ansible_user=deploy",
	)
	mustNotContain(t, got, fake, "p4ss")
	if !strings.HasSuffix(got[0], "R2_SECRET_ACCESS_KEY=***") {
		t.Fatalf("got %q", got[0])
	}
	if !strings.HasSuffix(got[2], "BETTER_AUTH_URL=https://example.test") {
		t.Fatalf("public URL should stay readable, got %q", got[2])
	}
	if !strings.HasSuffix(got[3], "ansible_user=deploy") {
		t.Fatalf("non-secret output should stay readable, got %q", got[3])
	}
}

func TestMultiLineSetEnvContinuationMasked(t *testing.T) {
	r := New(nil)
	got := feed(r,
		"[P/provision]   ⚙  ::set-env:: KAMAL_SSH_PRIVATE_KEY="+pemBegin,
		keyBody1,
		keyBody2,
		pemEnd,
		"[P/provision]   ✅  Success - Fetch secrets [1s]",
	)
	mustNotContain(t, got, keyBody1, keyBody2)
	if got[4] != "[P/provision]   ✅  Success - Fetch secrets [1s]" {
		t.Fatalf("job line after the value must pass through, got %q", got[4])
	}
}

func TestPEMBlockMasked(t *testing.T) {
	r := New(nil)
	got := feed(r,
		"[j/s]   | "+pemBegin,
		"[j/s]   | "+keyBody1,
		"[j/s]   | "+keyBody2,
		"[j/s]   | "+pemEnd,
		"[j/s]   | "+strings.Repeat("A", 40)+" after the block stays",
	)
	mustNotContain(t, got, keyBody1, keyBody2)
	if !strings.Contains(got[4], strings.Repeat("A", 40)) {
		t.Fatalf("lines after END must not be masked, got %q", got[4])
	}
}

func TestPEMCollapsedOnOneLine(t *testing.T) {
	got := String("[j/s] | " + pemBegin + " " + keyBody1 + " " + pemEnd)
	mustNotContain(t, []string{got}, keyBody1)
}

func TestURLPasswordMasked(t *testing.T) {
	got := String("connecting to postgresql://app_user:Sup3rS3cret@10.0.0.1:5432/db?sslmode=require")
	if got != "connecting to postgresql://app_user:***@10.0.0.1:5432/db?sslmode=require" {
		t.Fatalf("got %q", got)
	}
}

func TestTokenFormatsMasked(t *testing.T) {
	toks := []string{
		"dp.st.prd." + strings.Repeat("a", 40),
		"ops_" + strings.Repeat("b", 30),
		"sk-proj-" + strings.Repeat("c", 30),
		"sk_" + strings.Repeat("d", 30),
		"ghp_" + strings.Repeat("e", 36),
		"rwy_" + strings.Repeat("f", 32),
		"AKIA" + strings.Repeat("G", 16),
	}
	for _, tok := range toks {
		got := String("value=" + tok + " end")
		if strings.Contains(got, tok) {
			t.Errorf("token not masked: %q", got)
		}
	}
}

func TestOrdinaryLinesUntouched(t *testing.T) {
	lines := []string{
		"[Deploy/Deploy (stg)] ⭐ Run Main Fetch secrets from Doppler",
		"[Deploy/Deploy (stg)]   | Keys written to .env: 42",
		"[Deploy/Deploy (stg)]   | https://registry.aedatum.com/v2/ 200",
		"Error: Job 'deploy' failed",
	}
	r := New([]string{"some-known-secret"})
	for _, l := range lines {
		if got := r.Line(l); got != l {
			t.Errorf("changed %q -> %q", l, got)
		}
	}
}

func TestKnownValueEncodingsMasked(t *testing.T) {
	secret := "pa$$ w0rd/with+chars"
	multi := "first-line-secret\nsecond-line-secret"
	r := New([]string{secret, multi})
	cases := []string{
		base64.StdEncoding.EncodeToString([]byte(secret)),
		base64.RawURLEncoding.EncodeToString([]byte(secret)),
		url.QueryEscape(secret),
		url.PathEscape(secret),
		`{"v":"first-line-secret\nsecond-line-secret"}`,
	}
	for _, c := range cases {
		got := r.Line("[j/s] | " + c)
		for _, leak := range []string{secret, c, "first-line-secret", "second-line-secret"} {
			if strings.Contains(got, leak) {
				t.Errorf("encoding leaked: %q -> %q", c, got)
			}
		}
	}
}

func TestKeyBodyAfterMaskedHeader(t *testing.T) {
	// GitHub/act masked the header line but printed the body lines bare.
	r := New(nil)
	got := feed(r,
		"[P/prune]   KAMAL_SSH_PRIVATE_KEY: ***",
		keyBody1,
		"[P/prune]   | "+keyBody2,
	)
	mustNotContain(t, got, keyBody1, keyBody2)
	// hex digests and words must survive
	digest := "sha256:" + strings.Repeat("0123456789abcdef", 4)
	if out := r.Line("[P/prune]   | " + digest); !strings.Contains(out, digest) {
		t.Fatalf("hex digest masked: %q", out)
	}
}

func TestSetEnvValueRegisteredForLaterLines(t *testing.T) {
	r := New(nil)
	got := feed(r,
		"[D/d]   ⚙  ::set-env:: FACE_SERVICE_TOKEN=tok-"+strings.Repeat("z9", 8),
		"[D/d]   | calling with tok-"+strings.Repeat("z9", 8),
	)
	mustNotContain(t, got, "tok-"+strings.Repeat("z9", 8))
}

func TestURLPasswordWithQuotesAndSlashes(t *testing.T) {
	pw := `p'a"s/s`
	got := String("dsn=postgresql://u:" + pw + "@db:5432/x")
	if strings.Contains(got, pw) {
		t.Fatalf("password leaked: %q", got)
	}
}
