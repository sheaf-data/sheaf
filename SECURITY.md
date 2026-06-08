# Security Policy

## Supported versions

sheaf is pre-1.0. Security fixes land on the latest released `0.x` version and on `main`.

| Version              | Supported          |
| -------------------- | ------------------ |
| latest `0.x` release | ✅                 |
| older `0.x` releases | ❌ (upgrade first) |
| `main` (unreleased)  | ✅                 |

## Release integrity

Tagged releases (v0.1.0+) ship signed checksums and SBOMs. Signing is keyless
(Sigstore cosign) against the release workflow's GitHub OIDC identity; there are no
long-lived signing secrets in the repo. Verify a download before running it:
see [docs/verifying-releases.md](docs/verifying-releases.md).

## Reporting a vulnerability

**Please report security issues privately: do not open a public issue or PR.**

Use GitHub's private vulnerability reporting:

1. Open the **Security** tab of this repository.
2. Click **Report a vulnerability**.
3. Complete the advisory form.

This creates a private channel visible only to maintainers. If private reporting is
unavailable to you, you can request a secure contact channel by opening a minimal,
detail-free issue asking a maintainer to reach out.

### What to include

- A clear description of the issue and its impact.
- Steps to reproduce: a proof of concept, and the command / flag / config / repo
  characteristics needed to trigger it.
- Affected version or commit.
- Any suggested remediation.

## Response targets

| Stage                                          | Target                |
| ---------------------------------------------- | --------------------- |
| Acknowledge receipt                            | within 48 hours       |
| Initial assessment + severity                  | within 5 business days|
| Fix or mitigation for confirmed high/critical  | within 30 days        |
| Public disclosure                              | coordinated, post-fix |

We practice coordinated disclosure: we will keep you updated, credit you (unless you
prefer otherwise), and publish a GitHub Security Advisory once a fix ships.

## Scope

sheaf reads source repositories and serves an MCP endpoint. Reports in these areas are
especially valuable:

- Path traversal or file read/write outside the target repository.
- Code execution via crafted config (`sheaf.textproto`), manifests, or repo contents.
- `sheaf serve` (MCP) issues: unauthenticated access, crashes on malformed requests,
  resource exhaustion.
- Leakage of secrets (API keys, tokens) into reports, logs, or the snapshot cache.

Out of scope: vulnerabilities in third-party repositories you point sheaf at, and
findings that require a malicious local config you authored yourself.
