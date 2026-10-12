// Package redact scrubs secrets out of act output before Runway stores it.
//
// act honours ::add-mask:: for what it prints itself, but Runway keeps every
// line it reads from act's stdout in runway.db, so anything act or a step lets
// through (a multi-line key masked on one line only, a value written to
// GITHUB_ENV that act echoes back as `::set-env:: NAME=value`, a DSN rewritten
// after it was masked) would be stored and served by the logs API. A Redactor
// is the last line of defence: it replaces with *** every known secret value,
// every value registered through ::add-mask::, secret-named ::set-env:: /
// ::set-output:: values, PEM private-key bodies, URL passwords and well-known
// token formats.
package redact

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Mask is what every redacted value is replaced with (GitHub's convention).
const Mask = "***"

// minLen is the shortest known value we replace; shorter values ("1", "true",
// "stg") would turn ordinary words in the log into ***.
const minLen = 6

var (
	addMaskRe = regexp.MustCompile(`::add-mask::(.*)$`)
	// act logs every GITHUB_ENV / GITHUB_OUTPUT write as
	// "⚙  ::set-env:: NAME=value" / "::set-output:: name=value".
	setEnvRe = regexp.MustCompile(`(::set-(?:env|output)::\s*)([A-Za-z_][A-Za-z0-9_-]*)=(.*)$`)
	// strongName always marks a secret; weakName does unless publicName also
	// matches (R2_ACCESS_KEY_ID, BETTER_AUTH_URL, SSH_PUBLIC_KEY ...).
	strongName = regexp.MustCompile(`(?i)(secret|token|pass(w|$|_)|private|credential|dsn|database_url|db_url|cookie|jwt|signing)`)
	weakName   = regexp.MustCompile(`(?i)(api_?key|access_?key|_pem|auth|_key$|^key$|session)`)
	publicName = regexp.MustCompile(`(?i)(public|_id$|_url$|_user(name)?$|_dir$|_path$|_file$|site_key)`)
	// databaseURLName is a *_URL that carries a password even though "_url$"
	// normally marks a public value.
	databaseURLName = regexp.MustCompile(`(?i)(database|db|dsn|redis|amqp|mongo|postgres|connection)[a-z0-9_]*_url$`)

	pemBeginRe = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	pemEndRe   = regexp.MustCompile(`-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	base64Run  = regexp.MustCompile(`[A-Za-z0-9+/=]{16,}`)

	// scheme://user:password@host — keep the user, drop the password. The
	// password may hold quotes or slashes; it ends at the last '@' before
	// whitespace.
	urlPassRe = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^\s:/@'"]+:)([^\s]+)@`)

	// keyBodyLine is a line that is nothing but one long mixed-case base64
	// token: the body of a key whose PEM header was masked (or never printed),
	// e.g. a multi-line secret shown in a step's env block. Hex digests and
	// plain words never match (they lack one of upper/lower/digit, or are
	// shorter).
	keyBodyLine = regexp.MustCompile(`^(.*?[|:]\s*|\s*)([A-Za-z0-9+/]{40,}={0,2})\s*$`)

	tokenRes = []*regexp.Regexp{
		regexp.MustCompile(`\bdp\.(?:st|ct|sa|pt|scim|audit)\.[A-Za-z0-9_.-]{16,}`),
		regexp.MustCompile(`\bops_[A-Za-z0-9_]{16,}`),
		regexp.MustCompile(`\bsk-(?:proj-|ant-)?[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`\bsk_(?:live_|test_)?[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{30,}`),
		regexp.MustCompile(`\brwy_[A-Za-z0-9_]{16,}`),
		regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	}

	// jobPrefixRe matches act's "[Workflow/Job]" line prefix.
	// act prefixes job output with "[Workflow/Job]"; a value line such as an
	// INI "[default]" header has no slash and must not end masking.
	jobPrefixRe = regexp.MustCompile(`^\s*\[[^\]/]+/[^\]]+\]`)
	ansiRe      = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
)

// Redactor is stateful (PEM blocks and multi-line ::set-env:: values span
// lines), so use one per log stream. It is not safe for concurrent use.
type Redactor struct {
	values []string // longest first
	inPEM  bool
	// inSecretEnv is set after a secret-named ::set-env:: line: act prints the
	// remaining lines of a multi-line value without a [job] prefix.
	inSecretEnv bool
}

// New returns a Redactor that also replaces the given known secret values.
// Multi-line values are split so that every line is masked on its own.
func New(known []string) *Redactor {
	r := &Redactor{}
	for _, v := range known {
		r.add(v)
	}
	return r
}

// add registers a secret: each of its lines, and the common encodings a step
// might print it in (base64, URL/query escaping, JSON/Go string escaping of
// the whole value, so a multi-line value printed as one "a\nb" string is
// caught too).
func (r *Redactor) add(v string) {
	v = strings.ReplaceAll(v, "\r", "")
	trimmed := strings.TrimSpace(v)
	if len(trimmed) < minLen || trimmed == Mask {
		return
	}
	forms := strings.Split(v, "\n")
	for _, w := range []string{trimmed, strings.TrimSpace(forms[0])} {
		if len(w) < minLen {
			continue
		}
		forms = append(forms,
			base64.StdEncoding.EncodeToString([]byte(w)),
			base64.RawStdEncoding.EncodeToString([]byte(w)),
			base64.URLEncoding.EncodeToString([]byte(w)),
			base64.RawURLEncoding.EncodeToString([]byte(w)),
			url.QueryEscape(w),
			url.PathEscape(w),
		)
	}
	if q := strconv.Quote(trimmed); len(q) > 2 {
		forms = append(forms, q[1:len(q)-1])
	}
	seen := make(map[string]bool, len(r.values))
	for _, have := range r.values {
		seen[have] = true
	}
	for _, part := range forms {
		part = strings.TrimSpace(part)
		if len(part) < minLen || part == Mask || seen[part] {
			continue
		}
		seen[part] = true
		r.values = append(r.values, part)
	}
	sort.SliceStable(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
}

// Line returns s with every secret it can recognise replaced by ***.
func (r *Redactor) Line(s string) string {
	// Colour codes can split a value; match on the plain text, and keep the
	// original (colours intact) only when nothing needed masking.
	if strings.Contains(s, "\x1b[") {
		plain := ansiRe.ReplaceAllString(s, "")
		if out := r.line(plain); out != plain {
			return out
		}
		return s
	}
	return r.line(s)
}

func (r *Redactor) line(s string) string {
	// ::add-mask::value registers the value and must not be stored itself.
	if m := addMaskRe.FindStringSubmatchIndex(s); m != nil {
		r.add(s[m[2]:m[3]])
		return s[:m[2]] + Mask
	}

	if r.inSecretEnv {
		if jobPrefixRe.MatchString(s) {
			r.inSecretEnv = false
		} else {
			return Mask
		}
	}

	for _, v := range r.values {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, Mask)
		}
	}

	if m := setEnvRe.FindStringSubmatch(s); m != nil && isSecretName(m[2]) {
		// Remember the value so later echoes of it are masked too.
		r.add(m[3])
		s = setEnvRe.ReplaceAllString(s, "${1}${2}="+Mask)
		r.inSecretEnv = true
	}

	switch {
	case pemBeginRe.MatchString(s):
		r.inPEM = !pemEndRe.MatchString(s)
		s = maskAfter(s, pemBeginRe)
	case r.inPEM:
		if pemEndRe.MatchString(s) {
			r.inPEM = false
		}
		s = base64Run.ReplaceAllString(s, Mask)
	default:
		if m := keyBodyLine.FindStringSubmatch(s); m != nil && mixedCase(m[2]) {
			s = m[1] + Mask
		}
	}

	s = urlPassRe.ReplaceAllString(s, "${1}"+Mask+"@")
	for _, re := range tokenRes {
		s = re.ReplaceAllString(s, Mask)
	}
	return s
}

// String redacts a standalone message with a fresh Redactor (no known values).
func String(s string) string { return New(nil).Line(s) }

// mixedCase reports whether tok has an upper-case letter, a lower-case letter
// and a digit — true of random base64, false of hex digests and plain words.
func mixedCase(tok string) bool {
	var up, low, dig bool
	for _, c := range tok {
		switch {
		case c >= 'A' && c <= 'Z':
			up = true
		case c >= 'a' && c <= 'z':
			low = true
		case c >= '0' && c <= '9':
			dig = true
		}
	}
	return up && low && dig
}

func isSecretName(name string) bool {
	if databaseURLName.MatchString(name) || strongName.MatchString(name) {
		return true
	}
	return weakName.MatchString(name) && !publicName.MatchString(name)
}

// maskAfter replaces base64 runs that follow the PEM header on the same line
// (a key printed with its newlines collapsed).
func maskAfter(s string, re *regexp.Regexp) string {
	loc := re.FindStringIndex(s)
	return s[:loc[1]] + base64Run.ReplaceAllString(s[loc[1]:], Mask)
}
