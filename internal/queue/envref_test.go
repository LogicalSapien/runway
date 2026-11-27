package queue

import "testing"

func TestEnvRefReAcceptsTrailingComment(t *testing.T) {
	cases := map[string]string{
		"    environment: prd":                                "prd",
		"    environment: prd # protection = manual dispatch": "prd",
		"    environment: 'stg'   # quoted, then a comment":   "stg",
		"      name: prd":                                     "prd",
		"      name: \"prd\" # block form with comment":       "prd",
	}
	for line, want := range cases {
		m := envRefRe.FindStringSubmatch(line)
		if m == nil || m[1] != want {
			t.Errorf("%q: got %v, want %q", line, m, want)
		}
	}
}

func TestEnvRefReRejectsExpressionsAndHashInName(t *testing.T) {
	for _, line := range []string{
		"    environment: ${{ inputs.env }}",
		"    environment: prd#nospace",
		"    name: Deploy to prd",
	} {
		if m := envRefRe.FindStringSubmatch(line); m != nil {
			t.Errorf("%q: unexpected match %v", line, m)
		}
	}
}
