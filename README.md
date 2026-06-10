# Runway

**Self-hosted CI server that runs your GitHub Actions workflows on your own hardware — with a GitHub-compatible API and a real-time web UI.**

Runway is a lightweight wrapper around [act](https://github.com/nektos/act) (the GitHub Actions local runner). You keep writing standard `.github/workflows/*.yml` files; Runway gives you the missing server side: a dispatch API shaped like GitHub's, a run queue with concurrency control, live log streaming, run history, and user management — all in a single Go binary backed by SQLite.

## Why

- **Keep the GitHub Actions format, drop the GitHub Actions infrastructure.** Your workflows stay portable; your runners stay yours.
- **GitHub-compatible API.** Tools and scripts that call `POST /repos/{owner}/{repo}/actions/workflows/{file}/dispatches` work against Runway with a changed base URL and token.
- **Self-contained.** One binary, one SQLite file, the Docker daemon you already have. No Kubernetes, no message broker, no Postgres.

## Features

- **Workflow dispatch queue** — priority queue with a configurable concurrency limit, enforced at the DB level so restarts never over-schedule.
- **Live log streaming** — job/step lifecycle parsed from act output, streamed to the browser over SSE.
- **GitHub-compatible endpoints** — dispatch, list runs, get run, list jobs, fetch logs.
- **Repo management** — register repos by git URL (HTTPS or SSH with per-repo deploy keys), auto-discovery of workflow files, periodic polling.
- **Users, sessions, API keys** — admin/viewer roles, bcrypt passwords, HttpOnly session cookies, `rwy_` Bearer tokens for automation.
- **Resource limits** — per-run container memory/CPU caps passed through to Docker.
- **Retention** — automatic pruning of old runs and logs.
- **Mobile-friendly UI** — embedded static UI, no separate frontend deployment.

## Quick start

Requirements: Linux or macOS, Go ≥ 1.22 (with cgo), `git`, [act](https://github.com/nektos/act), and a running Docker daemon.

```bash
git clone https://github.com/aedatum/runway.git
cd runway
cp .env.example .env          # optional — defaults work for a local try-out
CGO_ENABLED=1 go build -o runway .
./runway
```

Open `http://localhost:8080/login.html` and sign in as `admin` / `runway` (or your `ADMIN_PASSWORD`). You'll be prompted to change the password, then:

1. **Register a repo** — Repos → Add, paste a git URL (use a deploy key for private SSH repos).
2. **Dispatch a workflow** — from the UI, or exactly like the GitHub API:

```bash
curl -X POST http://localhost:8080/repos/my-org/my-app/actions/workflows/ci.yml/dispatches \
  -H "Authorization: Bearer rwy_..." \
  -d '{"ref":"main","inputs":{"environment":"staging"}}'
```

3. **Watch it run** — the Runs tab streams logs live.

For production (systemd service, reverse proxy, TLS), see **[SETUP.md](SETUP.md)**.

## API

### GitHub-compatible

| Method | Path |
|---|---|
| `POST` | `/repos/{owner}/{repo}/actions/workflows/{workflow_file}/dispatches` |
| `GET` | `/repos/{owner}/{repo}/actions/runs` |
| `GET` | `/repos/{owner}/{repo}/actions/runs/{run_id}` |
| `GET` | `/repos/{owner}/{repo}/actions/runs/{run_id}/jobs` |
| `GET` | `/repos/{owner}/{repo}/actions/runs/{run_id}/logs` |
| `GET` | `/repos/{owner}/{repo}/actions/jobs/{job_id}` |
| `GET` | `/repos/{owner}/{repo}/actions/jobs/{job_id}/logs` |

Authenticate with `Authorization: Bearer <api key>` (create keys in the UI under Users → API keys).

### Runway-native

`/api/runs`, `/api/queue`, `/api/repos`, `/api/settings`, `/api/users`, `/api/keys`, `/api/steps/{id}/stream` (SSE), `/api/health` — see [internal/api/server.go](internal/api/server.go) for the full route table.

## Architecture

```
┌─────────┐   dispatch   ┌─────────┐   claim   ┌────────────┐   exec   ┌─────┐
│  UI/API │ ───────────► │  queue  │ ────────► │ engine     │ ───────► │ act │
│ (HTTP)  │              │ (SQLite)│           │ clone/pull │          │     │
└─────────┘              └─────────┘           └────────────┘          └──┬──┘
     ▲                                                                    │
     │              SSE ◄── parser ◄── stdout/stderr ◄────────────────────┘
```

- `internal/api` — HTTP handlers (GitHub-compatible + native), embedded UI.
- `internal/queue` — engine (DB-level concurrency), git clone/pull, act runner, output parser.
- `internal/poller` — periodic `git pull` + workflow discovery for registered repos.
- `internal/watcher` — optional Docker event watcher correlating act containers.
- `internal/auth` / `internal/middleware` — users, sessions, API keys, rate limiting.
- `internal/retention` — prunes old runs/logs per the `retention_days` setting.

## Security model

Read this before exposing Runway anywhere:

- **CI runs arbitrary code by design.** Anyone who can register a repo or dispatch a workflow can execute code in containers on your host, with the resource caps you configured. Treat admin and API-key credentials accordingly.
- Workflow filenames and git refs are validated before reaching the shell; deploy keys are written to `0600` temp files (never the environment) and removed after each run.
- SSH host keys are pinned on first use (`accept-new`).
- Serve over HTTPS via a reverse proxy — session cookies are `Secure` by default.

See [SECURITY.md](SECURITY.md) for the threat model and how to report vulnerabilities.

## Development

```bash
go build ./...
go vet ./...
go test ./...
```

CGO is required (go-sqlite3), so you need a C toolchain. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
