package queue

import (
	"strings"
	"testing"

	dbpkg "github.com/LogicalSapien/runway/internal/db"
)

func TestBuildArgsMergesContainerOptions(t *testing.T) {
	d := openTestDB(t)
	for k, v := range map[string]string{
		"act_container_options": "-v /home/ci/.ssh/key:/root/.ssh/id_ed25519:ro -v /etc/docker/certs.d:/etc/docker/certs.d:ro",
		"docker_memory":         "2g",
		"docker_cpus":           "2",
	} {
		if err := dbpkg.SetSetting(d, k, v); err != nil {
			t.Fatalf("set setting %s: %v", k, err)
		}
	}

	r := NewRunner(d, QueueItem{WorkflowFile: "ci.yml", RepoName: "app", Branch: "main"}, 1, "/tmp", "abc", "", "")
	args := r.buildArgs()

	// act accepts exactly one --container-options flag; operator mounts and
	// resource limits must arrive merged in it.
	var optValues []string
	for i, a := range args {
		if a == "--container-options" && i+1 < len(args) {
			optValues = append(optValues, args[i+1])
		}
	}
	if len(optValues) != 1 {
		t.Fatalf("got %d --container-options flags, want exactly 1 (args: %v)", len(optValues), args)
	}
	opts := optValues[0]
	for _, want := range []string{
		"-v /home/ci/.ssh/key:/root/.ssh/id_ed25519:ro",
		"-v /etc/docker/certs.d:/etc/docker/certs.d:ro",
		"--memory=2g",
		"--cpus=2",
		"--label act.dispatcher=runway",
		"--label act.repo=app",
	} {
		if !strings.Contains(opts, want) {
			t.Errorf("--container-options %q missing %q", opts, want)
		}
	}
}

func TestBuildArgsNoBareActLabelFlag(t *testing.T) {
	// act has no --label flag of its own (the legacy wrapper accepted one and
	// the real binary errors out on it) — labels must only ever appear inside
	// the --container-options value.
	d := openTestDB(t)
	r := NewRunner(d, QueueItem{WorkflowFile: "ci.yml", RepoName: "app", Branch: "main"}, 1, "/tmp", "abc", "", "")
	args := r.buildArgs()
	for i, a := range args {
		if a == "--label" {
			t.Fatalf("bare --label flag at args[%d] — real act rejects this (args: %v)", i, args)
		}
	}
}
