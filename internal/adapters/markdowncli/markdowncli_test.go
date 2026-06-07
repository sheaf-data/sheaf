package markdowncli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

func setupDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return dir
}

const dockerRunMD = `---
description: Create and run a new container from an image
keywords: run, docker, container
title: docker run
---

# docker run

<!---MARKER_GEN_START-->
Create and run a new container from an image.

### Options

| Name              | Type     | Default | Description                           |
|:------------------|:---------|:--------|:--------------------------------------|
| ` + "`--detach`" + ` | ` + "`bool`" + ` | false   | Run container in background |
<!---MARKER_GEN_END-->

## Description

The docker run command first creates a writeable container layer over the
specified image, and then starts it using the specified command. This is
roughly equivalent to ` + "`docker container create`" + ` followed by
` + "`docker container start`" + `.

## Examples

` + "```bash" + `
$ docker run --rm hello-world
` + "```" + `
`

const dockerContainerLsMD = `# docker container ls

List containers.
`

const dockerComposeUpMD = `# docker compose up

Builds, (re)creates, starts, and attaches to containers for a service.
`

func TestParse_FrontmatterTitle(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"docker_run.md": dockerRunMD,
	})
	a := New(Config{DocsDir: dir})
	claims, err := a.Parse(context.Background())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// docker_run.md contains a frontmatter title + one flag row +
	// one code fence. Expect a subcommand-level REFERENCE claim,
	// plus a per-flag REFERENCE claim, plus an EXAMPLE claim.
	sub := findSubcommandRef(claims, "docker run")
	if sub == nil {
		t.Fatalf("missing subcommand-level claim; refs=%v", claimRefs(claims))
	}
}

func TestParse_H1Fallback(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"x.md": dockerContainerLsMD,
	})
	a := New(Config{DocsDir: dir})
	claims, _ := a.Parse(context.Background())
	if len(claims) != 1 || claims[0].GetContractRefs()[0] != "docker container ls" {
		t.Errorf("H1 fallback failed; got %v", claimRefs(claims))
	}
}

func TestParse_FilenameFallback(t *testing.T) {
	body := "Some description without an H1 or frontmatter.\n"
	dir := setupDir(t, map[string]string{
		"docker_compose_up.md": body,
	})
	a := New(Config{DocsDir: dir})
	claims, _ := a.Parse(context.Background())
	if len(claims) != 1 || claims[0].GetContractRefs()[0] != "docker compose up" {
		t.Errorf("filename fallback failed; got %v", claimRefs(claims))
	}
}

// docker/cli's real layout: filenames are NOT prefixed with `docker_`,
// and nested files' H1 says just "# run" (not "# docker container run").
// With binary_name set, the adapter must prepend it regardless of H1.
func TestParse_BinaryNamePrependedForDockerCLILayout(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"run.md":           "# docker run\n\nCreate and run a new container from an image.\n",
		"container_run.md": "# run\n\nCreate and run a new container from an image.\n",
		"compose_up.md":    "# up\n\nBuilds and starts services.\n",
		"volume_create.md": "# create\n\nCreate a volume.\n",
	})
	a := New(Config{DocsDir: dir, BinaryName: "docker"})
	claims, _ := a.Parse(context.Background())
	refs := make(map[string]bool)
	for _, c := range claims {
		refs[c.GetContractRefs()[0]] = true
	}
	for _, want := range []string{
		"docker run",
		"docker container run",
		"docker compose up",
		"docker volume create",
	} {
		if !refs[want] {
			t.Errorf("missing %q; got %v", want, refs)
		}
	}
}

// kubernetes/website ships flag docs as inline HTML tables (not
// pipe-style markdown), one <tr> for the declaration + one <tr>
// for the description. Without this extractor, every kubectl flag
// looks undocumented even when the docs page lists it.
func TestParse_HTMLFlagTablesKubectlShape(t *testing.T) {
	const body = `---
title: kubectl apply
---

# kubectl apply

Apply a configuration to a resource by file name or stdin.

<table>
<tr>
<td colspan="2">--dry-run string[="unchanged"]&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Default: "none"</td>
</tr>
<tr>
<td></td><td style="line-height: 130%; word-wrap: break-word;"><p>Must be &quot;none&quot;, &quot;server&quot;, or &quot;client&quot;. Selects the dry-run strategy.</p></td>
</tr>

<tr>
<td colspan="2">-f, --filename strings</td>
</tr>
<tr>
<td></td><td style="line-height: 130%; word-wrap: break-word;"><p>The files that contain the configurations to apply.</p></td>
</tr>

<tr>
<td colspan="2">--force</td>
</tr>
<tr>
<td></td><td style="line-height: 130%; word-wrap: break-word;"><p>If true, immediately remove resources from API.</p></td>
</tr>
</table>
`
	dir := setupDir(t, map[string]string{"kubectl_apply.md": body})
	a := New(Config{DocsDir: dir, BinaryName: "kubectl"})
	claims, _ := a.Parse(context.Background())
	got := make(map[string]string)
	for _, c := range claims {
		if len(c.GetContractRefs()) > 0 {
			got[c.GetContractRefs()[0]] = c.GetRawText()
		}
	}
	for ref, mustContain := range map[string]string{
		"kubectl apply --dry-run":  "dry-run strategy",
		"kubectl apply --filename": "files that contain the configurations",
		"kubectl apply --force":    "immediately remove",
	} {
		raw, ok := got[ref]
		if !ok {
			t.Errorf("missing per-flag claim %q; got refs: %v", ref, mapKeys(got))
			continue
		}
		if !strings.Contains(raw, mustContain) {
			t.Errorf("claim %q raw text missing %q; got %q", ref, mustContain, raw)
		}
	}
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// URL_STYLE_FILE_PATH derives the URL from the on-disk path rather
// than the command tokens — required for Hugo/Jekyll sites like
// kubernetes.io where the URL preserves the dir name (underscores
// included) instead of splitting it.
func TestParse_URLStyleFilePathMatchesHugoLayout(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"kubectl_get/_index.md":                 "# kubectl get\n\nDisplay one or many resources.\n",
		"kubectl_config/_index.md":              "# kubectl config\n\nModify kubeconfig files.\n",
		"kubectl_config/kubectl_config_view.md": "# kubectl config view\n\nDisplay merged settings.\n",
		"kubectl.md":                            "# kubectl\n\nThe Kubernetes CLI root.\n",
	})
	a := New(Config{
		DocsDir:    dir,
		URLBase:    "https://kubernetes.io/docs/reference/kubectl/generated/",
		BinaryName: "kubectl",
		URLStyle:   URLStyleFilePath,
	})
	claims, _ := a.Parse(context.Background())
	byRef := make(map[string]string)
	for _, c := range claims {
		if len(c.GetContractRefs()) > 0 {
			byRef[c.GetContractRefs()[0]] = c.GetUrl()
		}
	}
	for ref, wantURL := range map[string]string{
		"kubectl get":         "https://kubernetes.io/docs/reference/kubectl/generated/kubectl_get/",
		"kubectl config":      "https://kubernetes.io/docs/reference/kubectl/generated/kubectl_config/",
		"kubectl config view": "https://kubernetes.io/docs/reference/kubectl/generated/kubectl_config/kubectl_config_view/",
		"kubectl":             "https://kubernetes.io/docs/reference/kubectl/generated/kubectl/",
	} {
		if got := byRef[ref]; got != wantURL {
			t.Errorf("URL for %q: got %q, want %q", ref, got, wantURL)
		}
	}
}

// Hugo's section convention: parent directories with subcommands get
// an `_index.md` page that describes the parent command itself.
// kubernetes/website ships kubectl reference docs this way:
// kubectl_get/_index.md ↔ "kubectl get". The adapter must derive
// the command from the directory name, not the literal "_index".
func TestParse_HugoIndexFilesResolveToParentDir(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"kubectl_get/_index.md":                 "# kubectl get\n\nDisplay one or many resources.\n",
		"kubectl_config/_index.md":              "# kubectl config\n\nModify kubeconfig files.\n",
		"kubectl_config/kubectl_config_view.md": "# kubectl config view\n\nDisplay merged kubeconfig settings.\n",
		"_index.md":                             "# kubectl section landing\n\nNot a command.\n",
	})
	a := New(Config{DocsDir: dir, BinaryName: "kubectl"})
	claims, _ := a.Parse(context.Background())
	got := make(map[string]bool)
	for _, c := range claims {
		got[c.GetContractRefs()[0]] = true
	}
	for _, want := range []string{
		"kubectl get",
		"kubectl config",
		"kubectl config view",
	} {
		if !got[want] {
			t.Errorf("missing %q; got %v", want, got)
		}
	}
	// The bare _index.md at the docs root has no enclosing command
	// directory and must NOT be claimed as "kubectl _index" or similar.
	for ref := range got {
		if strings.Contains(ref, "_index") {
			t.Errorf("unexpected _index claim leaked through: %q", ref)
		}
	}
}

// When binary_name matches the first filename segment, don't
// double-prefix. Covers `docker_run.md` produced by some other
// generator.
func TestParse_BinaryNameNotDoubled(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"docker_run.md": "# run\n\nDoc.\n",
	})
	a := New(Config{DocsDir: dir, BinaryName: "docker"})
	claims, _ := a.Parse(context.Background())
	if claims[0].GetContractRefs()[0] != "docker run" {
		t.Errorf("got %q, want docker run", claims[0].GetContractRefs()[0])
	}
}

func TestParse_URLConstruction(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"docker_container_run.md": "# docker container run\n\nDoc.\n",
		"docker.md":               "# docker\n\nRoot binary doc.\n",
	})
	a := New(Config{DocsDir: dir, URLBase: "https://docs.docker.com/reference/cli/"})
	claims, _ := a.Parse(context.Background())
	urls := make(map[string]string)
	for _, c := range claims {
		urls[c.GetContractRefs()[0]] = c.GetUrl()
	}
	if urls["docker container run"] != "https://docs.docker.com/reference/cli/container/run/" {
		t.Errorf("subcommand URL = %q", urls["docker container run"])
	}
	if urls["docker"] != "https://docs.docker.com/reference/cli/" {
		t.Errorf("root URL = %q", urls["docker"])
	}
}

func TestParse_URLBaseDefault(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"docker_container_run.md": "# docker container run\n",
	})
	a := New(Config{DocsDir: dir})
	claims, _ := a.Parse(context.Background())
	if !strings.HasPrefix(claims[0].GetUrl(), defaultURLBase) {
		t.Errorf("URL = %q does not start with default base", claims[0].GetUrl())
	}
}

func TestParse_SubstanceGrading(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"docker_run.md":          dockerRunMD,
		"docker_container_ls.md": dockerContainerLsMD,
		"docker_compose_up.md":   dockerComposeUpMD,
	})
	a := New(Config{DocsDir: dir})
	claims, _ := a.Parse(context.Background())
	// Substance grading applies to the subcommand-level REFERENCE
	// claim (one per file). Per-flag and EXAMPLE claims have their
	// own substance derived from cell/block content.
	run := findSubcommandRef(claims, "docker run")
	if run == nil || run.GetSubstance() != commonpb.Substance_SUBSTANTIVE {
		t.Errorf("docker run substance = %v, want SUBSTANTIVE", run.GetSubstance())
	}
	ls := findSubcommandRef(claims, "docker container ls")
	if ls == nil || ls.GetSubstance() == commonpb.Substance_ABSENT {
		t.Errorf("docker container ls substance = ABSENT, want graded")
	}
}

func TestParse_TableLinesStrippedFromProse(t *testing.T) {
	// Tables shouldn't bloat the word count.
	body := "# docker x\n\nShort.\n\n" +
		"| col | col |\n|---|---|\n| a | b |\n| c | d |\n"
	dir := setupDir(t, map[string]string{"x.md": body})
	a := New(Config{DocsDir: dir})
	claims, _ := a.Parse(context.Background())
	if got := claims[0].GetWordCount(); got > 3 {
		t.Errorf("word count = %d, want ≤3 (table should be stripped)", got)
	}
}

func TestParse_CodeFencesStrippedFromProse(t *testing.T) {
	body := "# docker x\n\nShort.\n\n```bash\n$ docker run --rm hello-world\n$ docker ps\n$ docker stop xxx\n```\n"
	dir := setupDir(t, map[string]string{"x.md": body})
	a := New(Config{DocsDir: dir})
	claims, _ := a.Parse(context.Background())
	// "docker x Short." → 3 tokens. Code-fence content (8+ words)
	// must not be counted.
	if got := claims[0].GetWordCount(); got > 4 {
		t.Errorf("word count = %d, want ≤4 (code fence should be stripped)", got)
	}
}

func TestParse_EmptyDirIsNoError(t *testing.T) {
	dir := setupDir(t, map[string]string{})
	a := New(Config{DocsDir: dir})
	claims, err := a.Parse(context.Background())
	if err != nil {
		t.Errorf("Parse: %v", err)
	}
	if len(claims) != 0 {
		t.Errorf("expected 0 claims; got %d", len(claims))
	}
}

func TestParse_MissingDocsDir(t *testing.T) {
	a := New(Config{DocsDir: ""})
	if _, err := a.Parse(context.Background()); err == nil {
		t.Error("expected error for empty docs_dir")
	}
}

func TestParse_KindIsReference(t *testing.T) {
	dir := setupDir(t, map[string]string{"docker_run.md": dockerRunMD})
	a := New(Config{DocsDir: dir})
	claims, _ := a.Parse(context.Background())
	if claims[0].GetKind() != docclaimpb.DocClaimKind_REFERENCE {
		t.Errorf("kind = %v, want REFERENCE", claims[0].GetKind())
	}
	if claims[0].GetAdapter() != Name {
		t.Errorf("adapter = %q, want %q", claims[0].GetAdapter(), Name)
	}
}

// findSubcommandRef returns the subcommand-level REFERENCE claim
// for ref (i.e. the claim whose only contract ref is exactly ref,
// with kind REFERENCE — not a per-flag or EXAMPLE claim).
func findSubcommandRef(claims []*docclaimpb.DocClaim, ref string) *docclaimpb.DocClaim {
	for _, c := range claims {
		if c.GetKind() != docclaimpb.DocClaimKind_REFERENCE {
			continue
		}
		refs := c.GetContractRefs()
		if len(refs) == 1 && refs[0] == ref {
			return c
		}
	}
	return nil
}

func TestParse_PerFlagClaimsFromOptionsTable(t *testing.T) {
	body := "# docker run\n\n### Options\n\n" +
		"| Name | Type | Default | Description |\n" +
		"|:-----|:-----|:--------|:------------|\n" +
		"| `--detach` | `bool` |   | Run container in background |\n" +
		"| `-a`, `--attach` | `list` |   | Attach to STDIN, STDOUT or STDERR |\n" +
		"| [`--add-host`](#add-host) | `list` |   | Add a custom host-to-IP mapping (host:ip) |\n" +
		"\n## Description\n\nCreate a new container.\n"
	dir := setupDir(t, map[string]string{"docker_run.md": body})
	a := New(Config{DocsDir: dir, BinaryName: "docker"})
	claims, _ := a.Parse(context.Background())
	got := map[string]bool{}
	for _, c := range claims {
		if c.GetKind() != docclaimpb.DocClaimKind_REFERENCE {
			continue
		}
		for _, r := range c.GetContractRefs() {
			got[r] = true
		}
	}
	for _, want := range []string{
		"docker run --detach",
		"docker run --attach",
		"docker run --add-host",
	} {
		if !got[want] {
			t.Errorf("missing per-flag claim for %q; got %v", want, got)
		}
	}
}

func TestParse_PerFlagURLConstructsAnchor(t *testing.T) {
	body := "# docker run\n\n### Options\n\n" +
		"| Name | Type | Default | Description |\n" +
		"|:-----|:-----|:--------|:------------|\n" +
		"| `--detach` | `bool` |   | Run container in background |\n"
	dir := setupDir(t, map[string]string{"docker_run.md": body})
	a := New(Config{DocsDir: dir, BinaryName: "docker", URLBase: "https://docs.docker.com/reference/cli/"})
	claims, _ := a.Parse(context.Background())
	for _, c := range claims {
		if len(c.GetContractRefs()) == 1 && c.GetContractRefs()[0] == "docker run --detach" {
			want := "https://docs.docker.com/reference/cli/run/#detach"
			if c.GetUrl() != want {
				t.Errorf("URL = %q, want %q", c.GetUrl(), want)
			}
			return
		}
	}
	t.Error("--detach claim not found")
}

func TestParse_InheritedOptionsHonored(t *testing.T) {
	body := "# docker run\n\n### Inherited options\n\n" +
		"| Name | Type | Default | Description |\n" +
		"|:-----|:-----|:--------|:------------|\n" +
		"| `--config` | `string` |   | Location of client config files |\n"
	dir := setupDir(t, map[string]string{"docker_run.md": body})
	a := New(Config{DocsDir: dir, BinaryName: "docker"})
	claims, _ := a.Parse(context.Background())
	for _, c := range claims {
		if len(c.GetContractRefs()) == 1 && c.GetContractRefs()[0] == "docker run --config" {
			return
		}
	}
	t.Errorf("expected docker run --config; got %v", claimRefs(claims))
}

func TestParse_NonOptionTableIgnored(t *testing.T) {
	// Some files have a "Subcommands" table that lists subcommands.
	// That's NOT a flag table — we mustn't emit "docker container --attach"
	// claims for entries like `[docker container attach](container_attach.md)`.
	body := "# docker container\n\n### Subcommands\n\n" +
		"| Name | Description |\n" +
		"|:-----|:------------|\n" +
		"| [`docker container attach`](container_attach.md) | Attach to a container |\n"
	dir := setupDir(t, map[string]string{"docker_container.md": body})
	a := New(Config{DocsDir: dir, BinaryName: "docker"})
	claims, _ := a.Parse(context.Background())
	for _, c := range claims {
		for _, r := range c.GetContractRefs() {
			if strings.Contains(r, "--") {
				t.Errorf("subcommand table produced a flag claim: %q", r)
			}
		}
	}
}

func TestParse_CodeFenceEmitsExampleClaim(t *testing.T) {
	body := "# docker run\n\n## Examples\n\n```bash\n$ docker run --rm hello-world\n```\n"
	dir := setupDir(t, map[string]string{"docker_run.md": body})
	a := New(Config{DocsDir: dir, BinaryName: "docker"})
	claims, _ := a.Parse(context.Background())
	gotExample := false
	for _, c := range claims {
		if c.GetKind() == docclaimpb.DocClaimKind_EXAMPLE &&
			len(c.GetContractRefs()) == 1 && c.GetContractRefs()[0] == "docker run" {
			gotExample = true
		}
	}
	if !gotExample {
		t.Errorf("expected at least one EXAMPLE claim; got %v", claims)
	}
}

func claimRefs(claims []*docclaimpb.DocClaim) []string {
	out := make([]string, 0, len(claims))
	for _, c := range claims {
		if len(c.GetContractRefs()) > 0 {
			out = append(out, c.GetContractRefs()[0])
		}
	}
	return out
}
