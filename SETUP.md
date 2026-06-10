# Runway — Setup & Operations Guide

Runway runs either as a **systemd service** on a Linux host (recommended — act
needs the host's Docker daemon anyway) or as a Docker container with the Docker
socket mounted. `scripts/install.sh` automates the systemd setup. This document
covers what it does, what to prepare manually, and how to adapt it to your host.

---

## Prerequisites (one-time per host)

| Requirement | Why | How to verify |
|---|---|---|
| Linux host with `sudo` access | Service install, package installs | — |
| Docker daemon running | act runs every job in containers | `docker ps` |
| The service user can run Docker | act is invoked as that user | `docker ps` as that user |
| Internet access (apt, go.dev, github.com) | install.sh downloads Go + act | `curl -s https://go.dev` |

---

## Quick install (systemd)

```bash
git clone https://github.com/LogicalSapien/runway.git
cd runway
cp .env.example .env        # edit ADMIN_PASSWORD and paths
bash scripts/install.sh
curl http://localhost:8080/api/health
# → {"status":"ok","time":"..."}
```

`install.sh` is idempotent — safe to re-run for updates (`git pull && bash scripts/install.sh`).

### What `install.sh` does

1. Installs **Go** (≥ 1.22) to `/usr/local/go` if missing or outdated
2. Installs **gcc** via apt (required for CGO/SQLite)
3. Installs **act** (the GitHub Actions local runner) to `/usr/local/bin/act`
4. Builds the runway binary with `CGO_ENABLED=1`
5. Generates and installs the **systemd unit** from `scripts/runway.service`,
   filling in your user and paths
6. Starts/restarts the service and verifies `/api/health`
7. Optionally updates **kamal-proxy** routing (only when `RUNWAY_DOMAIN` is set)

### Customizing the install

Everything is driven by environment variables — no script edits needed:

```bash
RUNWAY_USER=ci \
DATA_DIR=/var/lib/runway \
PORT=9090 \
bash scripts/install.sh
```

| Variable | Default | Purpose |
|---|---|---|
| `RUNWAY_USER` | current user | system user the service runs as |
| `RUNWAY_DIR` | repo root | checkout directory |
| `DATA_DIR` | `$HOME/runway-data` | SQLite DB + cloned repos |
| `PORT` | `8080` | HTTP port |
| `GO_VERSION` | `1.22.4` | Go toolchain installed if missing |
| `ACT_VERSION` | `0.2.67` | act release installed if missing |
| `RUNWAY_DOMAIN` | unset | public hostname for the optional kamal-proxy step |
| `RUNWAY_TLS_CERT` / `RUNWAY_TLS_KEY` | unset | TLS material for kamal-proxy |
| `SKIP_PROXY_UPDATE` | `0` | set `1` to skip the proxy step entirely |

---

## Reverse proxy

Runway listens on plain HTTP. Put any reverse proxy in front for TLS:

- **nginx / Caddy / Traefik** — proxy to `localhost:8080`, health check `/api/health`.
- **kamal-proxy** — `install.sh` can configure it: set `RUNWAY_DOMAIN` (and the
  TLS variables if the proxy terminates TLS). The target IP it uses is the
  Docker bridge gateway — the host as seen from inside the proxy container —
  detected automatically (override with `RUNWAY_HOST_IP`).

If the proxy runs in Docker, your firewall must allow the Docker bridge subnet
to reach the Runway port, e.g. for UFW:

```bash
SUBNET=$(docker network inspect bridge --format '{{range .IPAM.Config}}{{.Subnet}}{{end}}')
sudo ufw allow from "$SUBNET" to any port 8080 proto tcp
```

Session cookies are marked `Secure` by default, so serve the UI over HTTPS.
For plain-HTTP testing only, set `RUNWAY_INSECURE_COOKIES=true`.

---

## Environment variables (`.env`)

See [.env.example](.env.example) for the full annotated list. Summary:

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `8080` | HTTP port |
| `DB_PATH` | `./data/runway.db` | SQLite path — keep outside the checkout in production |
| `REPOS_ROOT` | `./data/repos` | where repos are cloned |
| `SECRETS_FILE` | empty | act `--secret-file`; optional |
| `ADMIN_PASSWORD` | `runway` | first-boot admin seed only |
| `ACT_PLATFORM_MAPPINGS` | `ubuntu-latest=catthehacker/ubuntu:act-22.04,…` | runner-label → image mappings (comma-separated in .env, one per line in the UI) |
| `ACT_CONTAINER_OPTIONS` | empty | extra docker options for job containers (mounts etc.) |
| `DOCKER_MEMORY` | `2g` | RAM cap per act container |
| `DOCKER_CPUS` | `2` | CPU cap per act container |
| `RUNWAY_INSECURE_COOKIES` | unset | plain-HTTP dev only |

`ADMIN_PASSWORD` is only used on first boot to seed the admin user. After that,
change passwords via the UI.

`SECRETS_FILE`, `ACT_PLATFORM_MAPPINGS`, `ACT_CONTAINER_OPTIONS`,
`DOCKER_MEMORY`, and `DOCKER_CPUS` are seeded into
the database on first boot and managed via **Settings** in the UI afterwards —
editing `.env` later has no effect unless you reset the database.

---

## First login

1. Navigate to `https://<your-domain>/login.html`
2. Log in as `admin` / `<ADMIN_PASSWORD from .env>` (default: `runway`)
3. You will be prompted to change the password on first login

---

## Disaster recovery (clean host)

```bash
git clone https://github.com/LogicalSapien/runway.git
cd runway
cp .env.example .env            # restore your settings
mkdir -p ~/runway-data/repos    # or restore DATA_DIR from backup
bash scripts/install.sh
curl http://localhost:8080/api/health
```

To preserve history, back up and restore the `DATA_DIR` (SQLite DB + repos)
before step 4. The database is a single file; stop the service before copying
it, or use `sqlite3 runway.db ".backup backup.db"` while running.
