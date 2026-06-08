# Verifying a release

Every tagged Sheaf release (v0.1.0 and later) ships signed checksums and a software bill of materials (SBOM), so you can confirm a download is authentic and unmodified before you run it.

The signing is **keyless** ([Sigstore](https://www.sigstore.dev/) cosign): there is no long-lived Sheaf private key to trust or leak. Instead, each artifact is signed using the release workflow's GitHub OIDC identity, with the certificate logged in the public [Rekor](https://docs.sigstore.dev/logging/overview/) transparency log. You verify against *who and what produced the artifact* (this repo's `release.yml` workflow, on a version tag), not against a key you have to obtain out of band.

## What's attached to each release

| Artifact | What it is |
|---|---|
| `sheaf_<version>_<os>_<arch>.tar.gz` / `.zip` | The binary archive. |
| `checksums.txt` | SHA-256 of every archive. |
| `checksums.txt.sig` + `checksums.txt.pem` | cosign signature + Fulcio certificate for `checksums.txt`. |
| `*.spdx.sbom.json` / `*.cdx.sbom.json` | Per-archive SBOM, SPDX and CycloneDX formats. |
| `*.spdx.sbom.json.sig` + `.pem` (etc.) | cosign signature + certificate for each SBOM. |

## Prerequisites

Install [cosign](https://docs.sigstore.dev/system_config/installation/) (v2+):

```sh
brew install cosign        # macOS
# or: go install github.com/sigstore/cosign/v2/cmd/cosign@latest
```

## Verify the download

Download the archive you want plus `checksums.txt`, `checksums.txt.sig`, and `checksums.txt.pem` from the [release page](https://github.com/sheaf-data/sheaf/releases), then:

```sh
# 1. Verify the signature on checksums.txt: proves it came from this repo's
#    release workflow, on a version tag, via the GitHub OIDC issuer.
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp '^https://github.com/sheaf-data/sheaf/\.github/workflows/release\.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  checksums.txt

# 2. With checksums.txt now trusted, verify the archive against it.
sha256sum --ignore-missing -c checksums.txt    # Linux
shasum -a 256 --ignore-missing -c checksums.txt # macOS
```

A clean run prints `Verified OK` from cosign and `<archive>: OK` from the checksum step. If either fails, **do not run the binary**: re-download, and if it still fails, [report it](../SECURITY.md).

## Verify an SBOM (optional)

Same pattern, pointing at the SBOM and its signature pair:

```sh
cosign verify-blob \
  --certificate sheaf_<version>_<os>_<arch>.tar.gz.spdx.sbom.json.pem \
  --signature sheaf_<version>_<os>_<arch>.tar.gz.spdx.sbom.json.sig \
  --certificate-identity-regexp '^https://github.com/sheaf-data/sheaf/\.github/workflows/release\.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  sheaf_<version>_<os>_<arch>.tar.gz.spdx.sbom.json
```

The SBOM lists every dependency baked into the binary; feed it to your own vulnerability scanner (`grype`, `trivy`) or policy tooling.

## How it's produced

The release pipeline ([`.goreleaser.yaml`](../.goreleaser.yaml) `sboms` + `signs` blocks, run from [`.github/workflows/release.yml`](../.github/workflows/release.yml)) generates SBOMs with [syft](https://github.com/anchore/syft) and signs the checksums and SBOMs with cosign keyless, using the workflow's `id-token: write` OIDC token. There are no signing secrets stored in the repository.
