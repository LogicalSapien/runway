FROM golang:1.25-bookworm AS builder

WORKDIR /build

RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc libsqlite3-dev && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o runway .

# ── Runtime ───────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git libsqlite3-0 && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/runway ./runway

RUN mkdir -p /data && \
    git config --global --add safe.directory '*'

ENV DB_PATH=/data/runway.db \
    REPOS_ROOT=/data/repos

EXPOSE 8080

HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -sf http://localhost:8080/api/health || exit 1

ENTRYPOINT ["/app/runway"]
