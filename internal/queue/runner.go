package queue

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/LogicalSapien/runway/internal/validate"
)

// Runner drives a single act invocation for one queue item.
type Runner struct {
	db          *sql.DB
	qi          QueueItem
	runID       int64
	clonePath   string
	sha         string
	secretsFile string // merged per-run secrets file (act --secret-file)
	varsFile    string // merged per-run variables file (act --var-file)
}

// NewRunner constructs a Runner.
func NewRunner(
	db *sql.DB,
	qi QueueItem,
	runID int64,
	clonePath, sha, secretsFile, varsFile string,
) *Runner {
	return &Runner{
		db:          db,
		qi:          qi,
		runID:       runID,
		clonePath:   clonePath,
		sha:         sha,
		secretsFile: secretsFile,
		varsFile:    varsFile,
	}
}

// Run spawns act, streams its stdout through the parser, and returns the process
// exit code along with any OS-level error.  A non-zero exit code does NOT
// produce an error — it is up to the caller to interpret the code.
func (r *Runner) Run(ctx context.Context) (exitCode int, err error) {
	// Re-validate even though the API layer already did: queue rows could have
	// been written by an older version or directly in the DB.
	if !validate.WorkflowFile(r.qi.WorkflowFile) {
		return 1, fmt.Errorf("invalid workflow file name: %q", r.qi.WorkflowFile)
	}

	args := r.buildArgs()
	env, cleanup := r.buildEnv()
	defer cleanup()

	cmd := exec.CommandContext(ctx, "act", args...)
	cmd.Dir = r.clonePath
	cmd.Env = env
	// On cancel, SIGTERM first so act tears down its containers; SIGKILL only
	// if it has not exited within the grace period.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second

	// CombinedOutput-style: both stdout and stderr feed the same pipe so the
	// parser sees lifecycle events (stdout) and error messages (stderr) together.
	pr, pw, err := os.Pipe()
	if err != nil {
		return 1, fmt.Errorf("pipe: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	stdout := pr

	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return 1, fmt.Errorf("act start: %w", err)
	}
	pw.Close() // parent no longer writes; reader will see EOF when child exits

	log.Printf("runner: act pid=%d run=%d repo=%s workflow=%s",
		cmd.Process.Pid, r.runID, r.qi.RepoName, r.qi.WorkflowFile)

	parser := NewParser(r.db, r.runID)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 512*1024), 512*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			break
		}
		parser.Feed(scanner.Text())
	}
	if scanErr := scanner.Err(); scanErr != nil {
		log.Printf("runner: scanner run=%d: %v", r.runID, scanErr)
	}

	parser.Flush()

	if waitErr := cmd.Wait(); waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return 1, waitErr
	}
	return 0, nil
}

// buildArgs constructs the act command-line arguments.
func (r *Runner) buildArgs() []string {
	event := r.qi.Event
	if event == "" {
		event = "workflow_dispatch"
	}
	args := []string{
		event,
		"-W", ".github/workflows/" + r.qi.WorkflowFile,
		// No -q: quiet mode also suppresses step stdout, leaving runs with
		// lifecycle lines but no actual job output to debug failures with.
	}

	if r.secretsFile != "" {
		if _, err := os.Stat(r.secretsFile); err == nil {
			args = append(args, "--secret-file", r.secretsFile)
		}
	}
	if r.varsFile != "" {
		if _, err := os.Stat(r.varsFile); err == nil {
			args = append(args, "--var-file", r.varsFile)
		}
	}

	// Built-in artifact server: upload/download-artifact actions work and the
	// files land under <artifacts_dir>/<runID> for the artifacts API to serve.
	if dir := r.artifactsDir(); dir != "" {
		args = append(args, "--artifact-server-path", dir)
	}

	// Forward workflow_dispatch inputs as act --input key=value flags.
	if r.qi.Inputs != "" {
		var inputs map[string]string
		if err := json.Unmarshal([]byte(r.qi.Inputs), &inputs); err == nil {
			for k, v := range inputs {
				args = append(args, "--input", k+"="+v)
			}
		}
	}

	// Platform mappings: one --platform label=image flag per line in the
	// act_platform_mappings setting. This maps every runner label our workflows
	// use (ubuntu-latest, self-hosted, custom labels, …) to the right image.
	for _, line := range r.platformMappings() {
		args = append(args, "--platform", line)
	}

	// Container options: act accepts only ONE --container-options flag — passing
	// ours used to silently override any operator value from ~/.actrc (losing
	// e.g. SSH-key and registry-cert mounts, which made every job container fail
	// and respawn). So the operator's extra options live in the
	// act_container_options setting and are merged with the resource limits here
	// into a single flag.
	containerOpts := r.containerOptions()
	if mem := r.dockerMemory(); mem != "" {
		containerOpts += fmt.Sprintf(" --memory=%s", mem)
	}
	if cpu := r.dockerCPUs(); cpu != "" {
		containerOpts += fmt.Sprintf(" --cpus=%s", cpu)
	}
	// Docker labels let the event watcher identify the run — and skip it: the
	// engine already tracks engine-dispatched runs, the watcher only captures
	// manual act invocations. act itself has no --label flag; docker create
	// options are the supported way to label job containers. The values are
	// validated upstream (no spaces/quotes), so plain interpolation is safe.
	containerOpts += fmt.Sprintf(
		" --label act.dispatcher=runway --label act.repo=%s --label act.branch=%s --label act.sha=%s",
		r.qi.RepoName, r.qi.Branch, r.sha,
	)
	args = append(args, "--container-options", strings.TrimSpace(containerOpts))

	return args
}

// buildEnv returns the environment for the act subprocess plus a cleanup
// function that removes the temp deploy-key file (call it after act exits).
func (r *Runner) buildEnv() (env []string, cleanup func()) {
	env = os.Environ()
	cleanup = func() {}

	if mem := r.dockerMemory(); mem != "" {
		env = setenv(env, "DOCKER_MEMORY", mem)
	}
	if cpu := r.dockerCPUs(); cpu != "" {
		env = setenv(env, "DOCKER_CPUS", cpu)
	}

	// Wire up the deploy key so act's git operations inside the workflow also
	// authenticate correctly.
	if r.qi.DeployKey != "" {
		if sshCmd, keyPath := sshCommand(r.qi.DeployKey); sshCmd != "" {
			env = setenv(env, "GIT_SSH_COMMAND", sshCmd)
			cleanup = func() { _ = os.Remove(keyPath) }
		}
	}

	return env, cleanup
}

// platformMappings reads the 'act_platform_mappings' setting and returns one
// "label=image" string per entry, ready to be passed as --platform flags.
// Entries are separated by newlines (Settings UI) or commas (.env files,
// where multiline values aren't possible).
func (r *Runner) platformMappings() []string {
	var v string
	_ = r.db.QueryRow(`SELECT value FROM settings WHERE key='act_platform_mappings'`).Scan(&v)
	var out []string
	for _, line := range strings.FieldsFunc(v, func(c rune) bool { return c == '\n' || c == ',' }) {
		line = strings.TrimSpace(line)
		if line != "" && strings.Contains(line, "=") {
			out = append(out, line)
		}
	}
	return out
}

// artifactsDir returns the per-run artifact directory when the artifacts_dir
// setting is configured ("" disables the act artifact server).
func (r *Runner) artifactsDir() string {
	var v string
	_ = r.db.QueryRow(`SELECT value FROM settings WHERE key='artifacts_dir'`).Scan(&v)
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	dir := filepath.Join(v, fmt.Sprintf("run-%d", r.runID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return dir
}

// containerOptions reads the optional 'act_container_options' setting — extra
// docker options for act job containers (e.g. volume mounts for deploy keys or
// registry certs). Merged with the memory/CPU limits into the single
// --container-options flag act accepts.
func (r *Runner) containerOptions() string {
	var v string
	_ = r.db.QueryRow(
		`SELECT value FROM settings WHERE key='act_container_options'`,
	).Scan(&v)
	return strings.TrimSpace(v)
}

// dockerMemory reads the optional 'docker_memory' setting (e.g. "4g").
// Returns "" if not set so the caller can skip the flag entirely.
func (r *Runner) dockerMemory() string {
	var v string
	_ = r.db.QueryRow(
		`SELECT value FROM settings WHERE key='docker_memory'`,
	).Scan(&v)
	return strings.TrimSpace(v)
}

// dockerCPUs reads the optional 'docker_cpus' setting (e.g. "2").
// Returns "" if not set so the caller can skip the flag entirely.
func (r *Runner) dockerCPUs() string {
	var v string
	_ = r.db.QueryRow(
		`SELECT value FROM settings WHERE key='docker_cpus'`,
	).Scan(&v)
	return strings.TrimSpace(v)
}

// now returns the current Unix timestamp (convenience helper used by parser).
func now() int64 { return time.Now().Unix() }
