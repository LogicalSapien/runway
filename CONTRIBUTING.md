# Contributing to Runway

Thanks for your interest! Issues and pull requests are welcome.

## Development setup

Requirements: Go ≥ 1.22 with a C toolchain (go-sqlite3 needs CGO), `git`,
Docker, and [act](https://github.com/nektos/act) if you want to exercise real
workflow runs.

```bash
git clone https://github.com/LogicalSapien/runway.git
cd runway
CGO_ENABLED=1 go build -o runway .
RUNWAY_INSECURE_COOKIES=true ./runway   # plain-HTTP cookies for local dev
```

The server uses relative defaults (`./data/runway.db`, `./data/repos`), so a
fresh checkout runs without configuration. Log in at
`http://localhost:8080/login.html` as `admin` / `runway`.

## Before you open a PR

```bash
gofmt -l .          # must print nothing
go vet ./...
go test ./...
```

- Keep PRs focused — one change per PR.
- Add or update tests for behavior changes, especially anything touching
  `internal/validate`, `internal/auth`, or `internal/queue`.
- Anything that ends up in a file path, command-line argument, or SQL query
  must be validated/parameterized — see `internal/validate` for the pattern.
- Match the existing code style (standard library first, small packages, no
  framework dependencies).

## Reporting bugs

Open a GitHub issue with reproduction steps, expected vs. actual behavior,
and your environment (OS, Go version, act version, Docker version).

For security vulnerabilities, **do not open a public issue** — see
[SECURITY.md](SECURITY.md).
