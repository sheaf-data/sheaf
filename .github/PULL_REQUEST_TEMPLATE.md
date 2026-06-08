<!--
Thanks for contributing to Sheaf. Keep one logical change per PR; smaller PRs
review faster. See CONTRIBUTING.md for the full flow.
-->

## What

<!-- One or two sentences: what does this change do? -->

## Why

<!-- The problem it solves, or the issue it closes. Link issues with "Closes #123". -->

## How it was tested

<!--
Show the evidence, not a reassurance. Paste the relevant output:
- code change → `make check` (build, gofmt, vet, golangci-lint, race tests)
- attribution change → before/after element or finding counts on a real repo
- adapter change → the fixture test you added
-->

## Checklist

- [ ] `make check` passes locally (build, `gofmt -l .` clean, vet, golangci-lint, `go test ./... -race -count=1`)
- [ ] Tests added/updated for new behavior
- [ ] Docs under `docs/` updated if behavior changed
- [ ] `KNOWN_LIMITATIONS.md` updated if this closes, adds, or moves a rough edge or scope line
- [ ] If `.proto` files changed: regenerated bindings (`proto/generate.sh`) are committed in this PR
