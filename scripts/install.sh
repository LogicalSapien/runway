#!/bin/bash
set -euo pipefail

# Runway install/update script (Linux host with systemd).
# Usage: bash scripts/install.sh
#
# Everything is overridable via environment variables:
#   RUNWAY_USER        system user the service runs as     (default: current user)
#   RUNWAY_DIR         checkout directory                  (default: this repo's root)
#   DATA_DIR           SQLite DB + cloned repos            (default: $HOME/runway-data)
#   PORT               HTTP port                           (default: 8080)
#   GO_VERSION         Go toolchain to install if missing  (default: 1.22.4)
#   ACT_VERSION        act release to install if missing   (default: 0.2.67)
#   SKIP_PROXY_UPDATE  set to 1 to skip the kamal-proxy step
#   RUNWAY_DOMAIN      public hostname for kamal-proxy routing (skipped if unset)
#   RUNWAY_TLS_CERT    TLS cert path inside the kamal-proxy container
#   RUNWAY_TLS_KEY     TLS key path inside the kamal-proxy container
#   RUNWAY_HOST_IP     override the detected Docker bridge gateway IP

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNWAY_USER="${RUNWAY_USER:-$(id -un)}"
RUNWAY_DIR="${RUNWAY_DIR:-$(dirname "${SCRIPT_DIR}")}"
DATA_DIR="${DATA_DIR:-${HOME}/runway-data}"
PORT="${PORT:-8080}"
GO_VERSION="${GO_VERSION:-1.22.4}"
ACT_VERSION="${ACT_VERSION:-0.2.67}"
SERVICE_FILE=/etc/systemd/system/runway.service

echo "=== Runway Install/Update ==="
echo "user=${RUNWAY_USER}  dir=${RUNWAY_DIR}  data=${DATA_DIR}  port=${PORT}"

# 0. Preflight: act runs every job through the Docker daemon — fail early if
# the service user can't reach it rather than failing on the first workflow.
if ! docker info &>/dev/null; then
  echo "ERROR: docker daemon not reachable as $(id -un)."
  echo "  Install Docker and add the user to the docker group: sudo usermod -aG docker ${RUNWAY_USER}"
  exit 1
fi
echo "docker: OK ($(docker --version))"

# 1. Ensure data dir exists
mkdir -p "${DATA_DIR}/repos"

# 2. Install Go if not present (or if < 1.21)
install_go() {
  echo "Installing Go ${GO_VERSION}..."
  cd /tmp
  wget -q "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf "go${GO_VERSION}.linux-amd64.tar.gz"
  rm "go${GO_VERSION}.linux-amd64.tar.gz"
  echo "Go installed"
}

if ! command -v go &>/dev/null; then
  install_go
else
  GO_MAJOR=$(go version | grep -o 'go[0-9]*.[0-9]*' | head -1 | sed 's/go//' | cut -d. -f1)
  GO_MINOR=$(go version | grep -o 'go[0-9]*.[0-9]*' | head -1 | sed 's/go//' | cut -d. -f2)
  if [ "${GO_MAJOR}" -lt 1 ] || ([ "${GO_MAJOR}" -eq 1 ] && [ "${GO_MINOR}" -lt 21 ]); then
    install_go
  else
    echo "Go $(go version) already installed"
  fi
fi

export PATH=$PATH:/usr/local/go/bin

# 3. Ensure gcc is present (required for CGO/go-sqlite3)
if ! command -v gcc &>/dev/null; then
  echo "Installing gcc (required for CGO)..."
  sudo apt-get update -qq
  sudo apt-get install -y -qq gcc
fi

# 3b. Install act if not present
install_act() {
  echo "Installing act ${ACT_VERSION}..."
  cd /tmp
  wget -q "https://github.com/nektos/act/releases/download/v${ACT_VERSION}/act_Linux_x86_64.tar.gz"
  sudo tar -C /usr/local/bin -xzf "act_Linux_x86_64.tar.gz" act
  rm "act_Linux_x86_64.tar.gz"
  echo "act installed: $(act --version)"
}

if ! command -v act &>/dev/null; then
  install_act
else
  echo "act already present — skipping install"
fi

# Verify act is the real binary, not a shell wrapper (a legacy log-capture
# wrapper chain once caused a process leak / fork bomb — never again).
if file "$(command -v act)" | grep -qi "script"; then
  echo "ERROR: $(command -v act) is a script, not the act binary."
  echo "  Remove it (sudo rm $(command -v act)) and re-run to install the real release."
  exit 1
fi
echo "act: OK ($(act --version))"

# 4. Build runway binary
echo "Building runway..."
cd "${RUNWAY_DIR}"
CGO_ENABLED=1 go build -o runway .
echo "Binary built: $(ls -lh runway | awk '{print $5}')"

# 5. Install systemd service (fill in user/paths from this environment)
echo "Installing systemd service..."
sed -e "s|__RUNWAY_USER__|${RUNWAY_USER}|g" \
    -e "s|__RUNWAY_DIR__|${RUNWAY_DIR}|g" \
    -e "s|__DATA_DIR__|${DATA_DIR}|g" \
    scripts/runway.service | sudo tee "${SERVICE_FILE}" >/dev/null
sudo systemctl daemon-reload
sudo systemctl enable runway

# 6. Start/restart service
if systemctl is-active runway &>/dev/null; then
  echo "Restarting runway..."
  sudo systemctl restart runway
else
  echo "Starting runway..."
  sudo systemctl start runway
fi

# 7. Wait and verify: service active AND the health endpoint answering.
for i in $(seq 1 15); do
  if curl -sf "http://localhost:${PORT}/api/health" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
if systemctl is-active runway &>/dev/null && curl -sf "http://localhost:${PORT}/api/health" >/dev/null; then
  echo "=== Runway is running ==="
  echo "health: $(curl -s http://localhost:${PORT}/api/health)"
  systemctl status runway --no-pager -l | head -8
else
  echo "=== ERROR: Runway failed to start or is unhealthy ==="
  systemctl status runway --no-pager -l | head -8 || true
  journalctl -u runway --no-pager -n 30
  exit 1
fi

# 8. Optional: update kamal-proxy routing to the host service.
# Only runs when RUNWAY_DOMAIN is set and a kamal-proxy container exists.
# The target IP is the Docker bridge gateway — the host as seen from inside
# the kamal-proxy container — detected dynamically unless RUNWAY_HOST_IP is set.
# Using nginx/Caddy/Traefik instead? Point it at localhost:${PORT} and ignore this.
if [ "${SKIP_PROXY_UPDATE:-0}" = "1" ] || [ -z "${RUNWAY_DOMAIN:-}" ]; then
  echo "Skipping kamal-proxy update (set RUNWAY_DOMAIN to enable)"
elif docker inspect kamal-proxy &>/dev/null; then
  HOST_IP="${RUNWAY_HOST_IP:-$(docker inspect kamal-proxy \
    --format '{{range .NetworkSettings.Networks}}{{.Gateway}}{{end}}' 2>/dev/null | \
    awk 'NF{print $1; exit}')}"
  if [ -z "${HOST_IP}" ]; then
    echo "WARNING: could not detect Docker bridge gateway — skipping proxy update"
    echo "  Set RUNWAY_HOST_IP=<ip> and re-run to configure kamal-proxy manually"
  else
    echo "Updating kamal-proxy routing → ${HOST_IP}:${PORT}..."
    TLS_FLAGS=()
    if [ -n "${RUNWAY_TLS_CERT:-}" ] && [ -n "${RUNWAY_TLS_KEY:-}" ]; then
      TLS_FLAGS=(--tls
        --tls-certificate-path="${RUNWAY_TLS_CERT}"
        --tls-private-key-path="${RUNWAY_TLS_KEY}")
    fi
    docker exec kamal-proxy kamal-proxy deploy runway \
      --target="${HOST_IP}:${PORT}" \
      --host="${RUNWAY_DOMAIN}" \
      "${TLS_FLAGS[@]}" \
      --deploy-timeout=60s \
      --health-check-path=/api/health
    echo "kamal-proxy routing updated (target: ${HOST_IP}:${PORT})"
  fi
else
  echo "WARNING: kamal-proxy container not found — skipping proxy update"
  echo "  If using a different reverse proxy, point it at localhost:${PORT}"
fi
