# Release-notes template

Copy this into `docs/releasing/vX.Y.Z.md` for each release and fill it in.
Pull the raw change list from [`CHANGELOG.md`](../../CHANGELOG.md); keep
this factual and let the maintainer do a voice pass before publishing it on
the GitHub release page.

---

# sheaf vX.Y.Z

<!-- One paragraph: the theme of this release in 2-3 sentences. What got
     better for a user, and why. -->

## Highlights

<!-- 2-5 bullets — the changes most users will care about. Lead with a
     verb; link each to its doc. -->

## Breaking changes

<!-- Config-schema, CLI-flag, or behavior changes that require user action.
     Link each to the matching upgrade-guide entry. Write "None." if none. -->

## Added

<!-- New features / adapters / commands (from CHANGELOG "Added"). -->

## Changed / Fixed

<!-- Behavior changes and bug fixes (from CHANGELOG "Changed"/"Fixed"). -->

## Install & verify

```bash
go install github.com/sheaf-data/sheaf/cmd/sheaf@vX.Y.Z
```

Release binaries are signed (cosign keyless) and ship SBOMs (SPDX +
CycloneDX) — verify per
[docs/verifying-releases.md](../verifying-releases.md).

## Thanks

<!-- Contributors since the previous tag:
     `git shortlog -sn vPREV..vX.Y.Z`. -->

**Full changelog:** https://github.com/sheaf-data/sheaf/compare/vPREV...vX.Y.Z
