package config

import (
	"os"
	"strings"
)

// DefaultPlatformMappings maps the ubuntu-* runner labels to the catthehacker
// images act recommends. Operators with custom labels (self-hosted, etc.) set
// their own mappings via ACT_PLATFORM_MAPPINGS or Settings → Platform mappings;
// without a mapping for its label, a job falls back to ~/.actrc or act's default.
const DefaultPlatformMappings = `ubuntu-latest=catthehacker/ubuntu:act-22.04
ubuntu-22.04=catthehacker/ubuntu:act-22.04`

type Config struct {
	Port             string
	DBPath           string
	ReposRoot        string
	RunnerDir        string
	AdminPassword    string
	SecretsFile      string // act --secret-file path; empty = no secrets file
	SecretsKey       string // hex master key for stored secrets; empty = keyfile next to the DB
	PlatformMappings string // newline-separated label=image pairs for act -P flags
	ContainerOpts    string // extra docker options for act job containers (volume mounts etc.)
	DockerMemory     string // memory limit for act containers (e.g. "2g")
	DockerCPUs       string // CPU limit for act containers (e.g. "2")
}

func Load() *Config {
	return &Config{
		Port:             getenv("PORT", "8080"),
		DBPath:           getenv("DB_PATH", "./data/runway.db"),
		ReposRoot:        getenv("REPOS_ROOT", "./data/repos"),
		RunnerDir:        getenv("RUNNER_DIR", "./data/runner"),
		AdminPassword:    getenv("ADMIN_PASSWORD", ""),
		SecretsFile:      getenv("SECRETS_FILE", ""),
		SecretsKey:       getenv("RUNWAY_SECRETS_KEY", ""),
		PlatformMappings: getenv("ACT_PLATFORM_MAPPINGS", DefaultPlatformMappings),
		ContainerOpts:    getenv("ACT_CONTAINER_OPTIONS", ""),
		DockerMemory:     getenv("DOCKER_MEMORY", "2g"),
		DockerCPUs:       getenv("DOCKER_CPUS", "2"),
	}
}

func getenv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}
