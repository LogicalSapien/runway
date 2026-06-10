// Package validate holds input validation shared by the API layer and the
// queue engine. Both validate the same values (defense in depth) because
// these strings end up in file paths and command-line arguments.
package validate

import (
	"regexp"
	"strings"
)

// workflowFileRe matches a bare workflow filename as it appears directly
// under .github/workflows/ — no directory separators, must end in .yml/.yaml.
var workflowFileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.ya?ml$`)

// refRe matches git branch/tag names accepted for dispatch. Leading
// alphanumeric prevents the value being parsed as a command-line flag.
var refRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/\-]*$`)

// WorkflowFile reports whether name is a safe workflow filename. It is later
// concatenated into the path passed to `act -W .github/workflows/<name>`, so
// path separators and parent references are rejected outright.
func WorkflowFile(name string) bool {
	if len(name) > 255 || strings.Contains(name, "..") {
		return false
	}
	return workflowFileRe.MatchString(name)
}

// Ref reports whether ref is a safe git branch/tag name. It is passed to
// `git checkout` / `git reset --hard origin/<ref>`, so parent references and
// flag-like values are rejected.
func Ref(ref string) bool {
	if ref == "" || len(ref) > 255 {
		return false
	}
	if strings.Contains(ref, "..") || strings.HasSuffix(ref, "/") || strings.Contains(ref, "//") {
		return false
	}
	return refRe.MatchString(ref)
}
