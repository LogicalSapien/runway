package queue

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/LogicalSapien/runway/internal/validate"
)

// CloneOrPull ensures the repo is present on disk and up-to-date, then returns
// (clonePath, headSHA, error).
//
// Logic:
//   - If clone_path is set and a .git directory exists there → git fetch + checkout + pull
//   - Otherwise → git clone into <reposDir>/<repoName>
//
// GIT_SSH_COMMAND is set when qi.DeployKey is non-empty so act's git operations
// also pick up the right key (the env var is inherited by child processes).
func CloneOrPull(qi QueueItem, reposDir string) (clonePath, sha string, err error) {
	clonePath = qi.ClonePath
	if clonePath == "" {
		clonePath = filepath.Join(reposDir, qi.RepoName)
	}

	if qi.Branch != "" && !validate.Ref(qi.Branch) {
		return clonePath, "", fmt.Errorf("invalid branch name: %q", qi.Branch)
	}

	env := os.Environ()
	if qi.DeployKey != "" {
		sshCmd, keyPath := sshCommand(qi.DeployKey)
		if sshCmd == "" {
			return clonePath, "", fmt.Errorf("deploy_key must be a PEM block (-----BEGIN …)")
		}
		defer os.Remove(keyPath)
		env = setenv(env, "GIT_SSH_COMMAND", sshCmd)
	}

	// Determine whether the clone exists.
	if _, statErr := os.Stat(filepath.Join(clonePath, ".git")); os.IsNotExist(statErr) {
		// Clone from scratch.
		if err = os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
			return clonePath, "", fmt.Errorf("mkdir %s: %w", filepath.Dir(clonePath), err)
		}
		if err = gitRun(env, "", "clone", "--branch", qi.Branch, "--", qi.GitURL, clonePath); err != nil {
			return clonePath, "", fmt.Errorf("git clone: %w", err)
		}
	} else {
		// Repo exists — fetch and reset to the requested branch.
		if err = gitRun(env, clonePath, "fetch", "origin"); err != nil {
			return clonePath, "", fmt.Errorf("git fetch: %w", err)
		}
		branch := qi.Branch
		if branch == "" {
			branch = "main"
		}
		if err = gitRun(env, clonePath, "checkout", branch); err != nil {
			return clonePath, "", fmt.Errorf("git checkout %s: %w", branch, err)
		}
		if err = gitRun(env, clonePath, "reset", "--hard", "origin/"+branch); err != nil {
			return clonePath, "", fmt.Errorf("git reset --hard origin/%s: %w", branch, err)
		}
	}

	sha, err = gitOutput(env, clonePath, "rev-parse", "HEAD")
	if err != nil {
		return clonePath, "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return clonePath, strings.TrimSpace(sha), nil
}

// sshCommand builds the GIT_SSH_COMMAND value for a deploy key and returns it
// together with the temp key file path. The caller must os.Remove(keyPath)
// once the git/act invocation is finished.
// deployKey must be a PEM block (-----BEGIN …). File paths are rejected to
// prevent injection via the GIT_SSH_COMMAND shell string.
func sshCommand(deployKey string) (cmd, keyPath string) {
	if !strings.HasPrefix(strings.TrimSpace(deployKey), "-----BEGIN") {
		// Only inline PEM blocks are accepted — reject file paths and anything else.
		return "", ""
	}
	f, err := os.CreateTemp("", "runway-deploy-key-*")
	if err != nil {
		return "", ""
	}
	_ = f.Chmod(0o600)
	_, _ = f.WriteString(deployKey)
	_ = f.Close()
	// Single-quote the path in the shell string so spaces/special chars can't break out.
	// Temp file names from os.CreateTemp use OS-chosen safe characters, but quoting
	// is cheap insurance and makes the intent explicit.
	escaped := strings.ReplaceAll(f.Name(), "'", "'\\''")
	// accept-new pins the host key on first contact and fails on later changes
	// (trust-on-first-use). Unlike =no this detects MITM after the first clone.
	return fmt.Sprintf("ssh -i '%s' -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes", escaped), f.Name()
}

// gitRun runs a git command, inheriting the supplied environment.
// dir may be "" for commands that do not require a working directory (e.g. clone).
func gitRun(env []string, dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Env = env
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// gitOutput runs a git command and returns trimmed stdout.
func gitOutput(env []string, dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Env = env
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// setenv returns a copy of env with key=value set (replacing any existing entry).
func setenv(env []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			result = append(result, e)
		}
	}
	return append(result, prefix+value)
}
