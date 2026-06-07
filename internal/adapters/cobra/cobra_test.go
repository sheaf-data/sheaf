package cobra

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

func setupYAMLDir(t *testing.T, files map[string]string) string {
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

const dockerRunYAML = `command: docker run
short: Create and run a new container from an image
long: |
  The docker run command first creates a writeable container layer over the
  specified image, and then starts it using the specified command.
usage: docker run [OPTIONS] IMAGE [COMMAND] [ARG...]
pname: docker
plink: docker.yaml
options:
  - option: detach
    shorthand: d
    value_type: bool
    default_value: "false"
    description: Run container in background and print container ID
    deprecated: false
    hidden: false
  - option: name
    value_type: string
    description: Assign a name to the container
inherited_options:
  - option: help
    value_type: bool
    description: Print usage
    hidden: true
deprecated: false
experimental: false
`

const dockerContainerLsYAML = `command: docker container ls
short: List containers
options:
  - option: all
    shorthand: a
    value_type: bool
    description: Show all containers (default shows just running)
  - option: filter
    shorthand: f
    value_type: filter
    description: Filter output based on conditions provided
`

func TestDiscover_TopLevelAndSubcommand(t *testing.T) {
	dir := setupYAMLDir(t, map[string]string{
		"docker_run.yaml":          dockerRunYAML,
		"docker_container_ls.yaml": dockerContainerLsYAML,
	})
	a := New(Config{YAMLDir: dir, BinaryName: "docker"})
	elems, err := a.Discover(context.Background(), "", adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got := elemIDs(elems)
	want := []string{
		"docker container ls",
		"docker container ls --all",
		"docker container ls --filter",
		"docker run",
		"docker run --detach",
		"docker run --name",
	}
	if !sliceEq(got, want) {
		t.Errorf("ids:\n got: %v\nwant: %v", got, want)
	}
}

func TestDiscover_BoolFlagIsSwitch(t *testing.T) {
	dir := setupYAMLDir(t, map[string]string{
		"docker_run.yaml": dockerRunYAML,
	})
	a := New(Config{YAMLDir: dir, BinaryName: "docker"})
	elems, _ := a.Discover(context.Background(), "", adapters.ScopeConfig{})
	detach := findElem(elems, "docker run --detach")
	name := findElem(elems, "docker run --name")
	if detach == nil || detach.GetKind() != contractpb.ContractElementKind_SWITCH {
		t.Errorf("--detach kind = %v, want SWITCH", detach.GetKind())
	}
	if name == nil || name.GetKind() != contractpb.ContractElementKind_FLAG {
		t.Errorf("--name kind = %v, want FLAG", name.GetKind())
	}
}

func TestDiscover_DocsAttached(t *testing.T) {
	dir := setupYAMLDir(t, map[string]string{
		"docker_run.yaml": dockerRunYAML,
	})
	a := New(Config{YAMLDir: dir, BinaryName: "docker"})
	elems, _ := a.Discover(context.Background(), "", adapters.ScopeConfig{})
	sub := findElem(elems, "docker run")
	if sub == nil {
		t.Fatal("missing docker run")
	}
	if !strings.Contains(sub.GetDocCommentExcerpt(), "writeable container layer") {
		t.Errorf("subcommand doc = %q", sub.GetDocCommentExcerpt())
	}
	detach := findElem(elems, "docker run --detach")
	if !strings.Contains(detach.GetDocCommentExcerpt(), "Run container in background") {
		t.Errorf("--detach doc = %q", detach.GetDocCommentExcerpt())
	}
}

func TestDiscover_LibraryAndEcosystem(t *testing.T) {
	dir := setupYAMLDir(t, map[string]string{
		"docker_container_ls.yaml": dockerContainerLsYAML,
	})
	a := New(Config{YAMLDir: dir, BinaryName: "docker"})
	elems, _ := a.Discover(context.Background(), "", adapters.ScopeConfig{})
	for _, e := range elems {
		if e.GetLibrary() != "docker" {
			t.Errorf("%s library = %q, want docker", e.GetId(), e.GetLibrary())
		}
		if e.GetEcosystem() != "cobra" {
			t.Errorf("%s ecosystem = %q, want cobra", e.GetId(), e.GetEcosystem())
		}
	}
}

func TestDiscover_FilenameFallback(t *testing.T) {
	// File omits the `command:` key — adapter must fall back to filename.
	body := `short: List containers
options:
  - option: all
    value_type: bool
    description: Show all
`
	dir := setupYAMLDir(t, map[string]string{
		"docker_container_ls.yaml": body,
	})
	a := New(Config{YAMLDir: dir, BinaryName: "docker"})
	elems, _ := a.Discover(context.Background(), "", adapters.ScopeConfig{})
	if findElem(elems, "docker container ls") == nil {
		t.Errorf("expected docker container ls; got %v", elemIDs(elems))
	}
}

func TestDiscover_ScopeExcludesBinary(t *testing.T) {
	dir := setupYAMLDir(t, map[string]string{
		"docker_run.yaml": dockerRunYAML,
	})
	a := New(Config{YAMLDir: dir, BinaryName: "docker"})
	elems, _ := a.Discover(context.Background(), "", adapters.ScopeConfig{
		Libraries: []string{"podman"},
	})
	if len(elems) != 0 {
		t.Errorf("expected no elements when binary not in scope; got %d", len(elems))
	}
}

func TestDiscover_EmptyYAMLDir(t *testing.T) {
	a := New(Config{YAMLDir: "", BinaryName: "docker"})
	_, err := a.Discover(context.Background(), "", adapters.ScopeConfig{})
	if err == nil {
		t.Error("expected error for empty yaml_dir")
	}
}

func TestDiscover_LocationCarriesPath(t *testing.T) {
	dir := setupYAMLDir(t, map[string]string{
		"docker_run.yaml": dockerRunYAML,
	})
	a := New(Config{YAMLDir: dir, BinaryName: "docker"})
	elems, _ := a.Discover(context.Background(), "", adapters.ScopeConfig{})
	sub := findElem(elems, "docker run")
	if sub == nil || sub.GetLocation() == nil || sub.GetLocation().GetPath() == "" {
		t.Errorf("location.path empty; got %+v", sub.GetLocation())
	}
}

func TestDiscover_AliasesPopulated(t *testing.T) {
	// docker/cli's actual output style: each YAML lists itself + all
	// alternate forms in `aliases:` as a comma-separated string.
	containerLs := `command: docker container ls
aliases: docker container ls, docker container list, docker container ps, docker ps
short: List containers
options:
  - option: all
    value_type: bool
    description: Show all
`
	dir := setupYAMLDir(t, map[string]string{
		"docker_container_ls.yaml": containerLs,
	})
	a := New(Config{YAMLDir: dir, BinaryName: "docker"})
	elems, _ := a.Discover(context.Background(), "", adapters.ScopeConfig{})
	sub := findElem(elems, "docker container ls")
	if sub == nil {
		t.Fatal("missing docker container ls")
	}
	got := map[string]bool{}
	for _, a := range sub.GetAliases() {
		got[a] = true
	}
	for _, want := range []string{
		"docker container ls",
		"docker container list",
		"docker container ps",
		"docker ps",
	} {
		if !got[want] {
			t.Errorf("missing alias %q; got %v", want, sub.GetAliases())
		}
	}
}

func TestDiscover_FlagAliasesMirrorParentAliases(t *testing.T) {
	containerLs := `command: docker container ls
aliases: docker container ls, docker container list, docker ps
short: List containers
options:
  - option: all
    value_type: bool
    description: Show all
`
	dir := setupYAMLDir(t, map[string]string{
		"docker_container_ls.yaml": containerLs,
	})
	a := New(Config{YAMLDir: dir, BinaryName: "docker"})
	elems, _ := a.Discover(context.Background(), "", adapters.ScopeConfig{})
	flag := findElem(elems, "docker container ls --all")
	if flag == nil {
		t.Fatal("missing flag")
	}
	wantAliases := map[string]bool{
		"docker container ls --all":   true,
		"docker container list --all": true,
		"docker ps --all":             true,
	}
	for _, a := range flag.GetAliases() {
		if !wantAliases[a] {
			t.Errorf("unexpected flag alias %q", a)
		}
		delete(wantAliases, a)
	}
	for missing := range wantAliases {
		t.Errorf("missing flag alias %q", missing)
	}
}

func TestDiscover_DedupMutualAliasYAMLs(t *testing.T) {
	// Cobra's GenYamlTree emits one YAML per registered command,
	// so a command available at both top-level and namespaced paths
	// produces two YAMLs. They cross-reference each other in
	// `aliases:`. The adapter should keep exactly one (the deeper
	// path) and treat the other as an alias.
	dockerRun := `command: docker run
aliases: docker container run, docker run
short: Run a container
options:
  - option: detach
    value_type: bool
    description: Detach
`
	dockerContainerRun := `command: docker container run
aliases: docker container run, docker run
short: Run a container
options:
  - option: detach
    value_type: bool
    description: Detach
`
	dir := setupYAMLDir(t, map[string]string{
		"docker_run.yaml":           dockerRun,
		"docker_container_run.yaml": dockerContainerRun,
	})
	a := New(Config{YAMLDir: dir, BinaryName: "docker"})
	elems, _ := a.Discover(context.Background(), "", adapters.ScopeConfig{})

	// Should keep only docker container run (deeper path), with
	// docker run as an alias. So the canonical SUBCOMMAND id is
	// "docker container run" — and there should NOT be a separate
	// "docker run" element.
	if findElem(elems, "docker container run") == nil {
		t.Errorf("missing canonical docker container run")
	}
	if findElem(elems, "docker run") != nil {
		t.Errorf("did not expect docker run as a separate element after dedup; got it")
	}
	sub := findElem(elems, "docker container run")
	gotAliases := map[string]bool{}
	for _, a := range sub.GetAliases() {
		gotAliases[a] = true
	}
	for _, want := range []string{"docker container run", "docker run"} {
		if !gotAliases[want] {
			t.Errorf("expected alias %q; got %v", want, sub.GetAliases())
		}
	}
	// Only one --detach flag element (the canonical), with aliases
	// for the short form.
	if findElem(elems, "docker container run --detach") == nil {
		t.Errorf("missing flag")
	}
	if findElem(elems, "docker run --detach") != nil {
		t.Errorf("did not expect docker run --detach after dedup")
	}
}

func TestDiscover_NoMutualAliasNoDedup(t *testing.T) {
	// Two unrelated commands with no overlapping aliases: each
	// stays as its own canonical element.
	build := `command: docker build
short: Build
`
	pull := `command: docker pull
short: Pull
`
	dir := setupYAMLDir(t, map[string]string{
		"docker_build.yaml": build,
		"docker_pull.yaml":  pull,
	})
	a := New(Config{YAMLDir: dir, BinaryName: "docker"})
	elems, _ := a.Discover(context.Background(), "", adapters.ScopeConfig{})
	if findElem(elems, "docker build") == nil || findElem(elems, "docker pull") == nil {
		t.Errorf("expected both elements; got %v", elemIDs(elems))
	}
}

func TestCommandPathFromFilename(t *testing.T) {
	cases := []struct {
		file, binary, want string
	}{
		{"docker_run.yaml", "docker", "docker run"},
		{"docker_compose_up.yaml", "docker", "docker compose up"},
		{"container_ls.yaml", "docker", "docker container ls"},
		{"docker.yaml", "docker", "docker"},
		// Already prefixed — must not double-prefix.
		{"docker_container_ls.yaml", "docker", "docker container ls"},
	}
	for _, c := range cases {
		got := commandPathFromFilename(c.file, c.binary)
		if got != c.want {
			t.Errorf("commandPathFromFilename(%q,%q) = %q, want %q", c.file, c.binary, got, c.want)
		}
	}
}

func elemIDs(elems []*contractpb.ContractElement) []string {
	out := make([]string, len(elems))
	for i, e := range elems {
		out[i] = e.GetId()
	}
	sort.Strings(out)
	return out
}

func findElem(elems []*contractpb.ContractElement, id string) *contractpb.ContractElement {
	for _, e := range elems {
		if e.GetId() == id {
			return e
		}
	}
	return nil
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
