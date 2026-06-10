# Security Policy

## Reporting a vulnerability

Please report vulnerabilities privately via
[GitHub Security Advisories](https://github.com/aedatum/runway/security/advisories/new)
("Report a vulnerability" on the repo's Security tab). Do not open a public
issue for security problems.

You can expect an acknowledgement within a few days. Please include
reproduction steps and the version/commit you tested.

## Threat model — what Runway does and doesn't protect against

Runway is a CI server: **executing code supplied by repositories is its
purpose**, not a vulnerability. The boundaries you should understand:

- **Anyone with dispatch access can run code on your host** (inside Docker
  containers, with the configured memory/CPU caps). Admins additionally
  control which repos and deploy keys are registered. Protect credentials
  and API keys accordingly, and put Runway behind HTTPS.
- **Workflows can reach the Docker daemon's resources.** act containers run
  on the host's Docker daemon. A malicious workflow has the same blast
  radius as `docker run` for the service user. Do not run Runway on a host
  whose Docker daemon you wouldn't hand to your CI jobs.
- **Secrets in `SECRETS_FILE` are visible to every workflow** that runs,
  by design (act's `--secret-file`). Scope them to CI use.
- **Deploy keys** are stored in the SQLite database (plaintext at rest) and
  written to `0600` temp files for the duration of a run. Anyone with read
  access to the database file or root on the host can read them — protect
  `DB_PATH` with filesystem permissions and disk encryption as appropriate.

## Hardening checklist

- Serve through a TLS-terminating reverse proxy; never set
  `RUNWAY_INSECURE_COOKIES` in production.
- Change the admin password on first login (Runway forces this) and create
  least-privilege `viewer` users for read-only access.
- Keep `DB_PATH` and `REPOS_ROOT` outside the checkout, readable only by the
  service user.
- Use per-repo deploy keys with read-only access instead of account-wide SSH
  keys.
- Keep `act`, Docker, and the runner images up to date.
