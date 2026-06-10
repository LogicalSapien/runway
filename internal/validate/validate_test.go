package validate

import (
	"strings"
	"testing"
)

func TestWorkflowFile(t *testing.T) {
	valid := []string{
		"ci.yml",
		"deploy.yaml",
		"build-and-test.yml",
		"release_v2.yml",
		"a.yml",
		"My.Workflow.yaml",
	}
	for _, name := range valid {
		if !WorkflowFile(name) {
			t.Errorf("WorkflowFile(%q) = false, want true", name)
		}
	}

	invalid := []string{
		"",
		"ci",
		"ci.yml.txt",
		"ci.json",
		"../../../etc/passwd",
		"../secrets.yml",
		"..%2Fci.yml",
		"sub/dir.yml",
		"/etc/passwd.yml",
		".yml",
		".hidden.yml",
		"-flag.yml",
		"a..b.yml",
		"ci.yml\n",
		strings.Repeat("a", 256) + ".yml",
	}
	for _, name := range invalid {
		if WorkflowFile(name) {
			t.Errorf("WorkflowFile(%q) = true, want false", name)
		}
	}
}

func TestRef(t *testing.T) {
	valid := []string{
		"main",
		"feature/login-page",
		"release-1.2.3",
		"v2.0",
		"user/nested/branch",
		"1234",
	}
	for _, ref := range valid {
		if !Ref(ref) {
			t.Errorf("Ref(%q) = false, want true", ref)
		}
	}

	invalid := []string{
		"",
		"-rf",
		"--force",
		"branch with spaces",
		"branch;rm -rf /",
		"a..b",
		"../other",
		"branch/",
		"a//b",
		"branch\nname",
		"$(whoami)",
		strings.Repeat("a", 256),
	}
	for _, ref := range invalid {
		if Ref(ref) {
			t.Errorf("Ref(%q) = true, want false", ref)
		}
	}
}
