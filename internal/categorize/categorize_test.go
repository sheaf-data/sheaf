package categorize

import (
	"reflect"
	"testing"

	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
)

func mkRules(cats ...*categorizationpb.Category) *categorizationpb.Rules {
	return &categorizationpb.Rules{Version: 1, Category: cats}
}

func TestCategorize_SingleMatch(t *testing.T) {
	c, err := New(mkRules(
		&categorizationpb.Category{
			DottedPath: "tests.unit_tests",
			Paths:      []string{"src/**/*_test.cc"},
		},
	))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := c.Categorize("src/foo/bar_test.cc", nil)
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	want := []string{"tests.unit_tests"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCategorize_NoMatch(t *testing.T) {
	c, _ := New(mkRules(
		&categorizationpb.Category{
			DottedPath: "tests.unit_tests",
			Paths:      []string{"src/**/*_test.cc"},
		},
	))
	got, err := c.Categorize("README.md", nil)
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestCategorize_MultiBucket(t *testing.T) {
	c, _ := New(mkRules(
		&categorizationpb.Category{
			DottedPath: "tests.integration_tests",
			Paths:      []string{"**/integration/**/*.cc"},
		},
		&categorizationpb.Category{
			DottedPath: "tests.golden",
			Paths:      []string{"**/golden/**/*.cc"},
		},
	))
	got, err := c.Categorize("src/foo/integration/golden/render_test.cc", nil)
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	// Both buckets, sorted.
	want := []string{"tests.golden", "tests.integration_tests"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCategorize_ExcludePathWins(t *testing.T) {
	c, _ := New(mkRules(
		&categorizationpb.Category{
			DottedPath:   "tests.unit_tests",
			Paths:        []string{"src/**/*_test.cc"},
			ExcludePaths: []string{"src/**/integration_tests/**"},
		},
	))
	got, err := c.Categorize("src/foo/integration_tests/bar_test.cc", nil)
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	if got != nil {
		t.Errorf("expected exclude to suppress match, got %v", got)
	}
}

func TestCategorize_DocsReferenceSkipped(t *testing.T) {
	// docs.reference has no `paths` — should never be path-matched.
	c, _ := New(mkRules(
		&categorizationpb.Category{DottedPath: "docs.reference"},
		&categorizationpb.Category{
			DottedPath: "docs.tutorials",
			Paths:      []string{"docs/tutorials/**/*.md"},
		},
	))
	got, _ := c.Categorize("docs/tutorials/getting-started.md", nil)
	want := []string{"docs.tutorials"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCategorize_AdapterPopulated(t *testing.T) {
	c, _ := New(mkRules(
		&categorizationpb.Category{DottedPath: "docs.reference"},
		&categorizationpb.Category{DottedPath: "docs.tutorials", Paths: []string{"docs/**/*.md"}},
		&categorizationpb.Category{DottedPath: "usage.internal"},
	))
	got := c.AdapterPopulated()
	want := []string{"docs.reference", "usage.internal"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCategorize_AllDeclared(t *testing.T) {
	c, _ := New(mkRules(
		&categorizationpb.Category{DottedPath: "tests.unit_tests", Paths: []string{"*"}},
		&categorizationpb.Category{DottedPath: "docs.reference"},
	))
	got := c.AllDeclared()
	want := []string{"docs.reference", "tests.unit_tests"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCategorize_SectionExcludesDropsMatch(t *testing.T) {
	c, _ := New(mkRules(&categorizationpb.Category{
		DottedPath:      "docs.concepts",
		Paths:           []string{"README.md"},
		SectionExcludes: []string{"command-line options", "options"},
	}))
	// Heading-context that should exclude.
	got, sectionExcluded, err := c.CategorizeWithDecision(
		"README.md", []string{"fd", "How to use", "Command-line options"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty cats; got %v", got)
	}
	if !sectionExcluded {
		t.Errorf("expected sectionExcluded=true")
	}
	// Heading-context that should pass.
	got2, sectionExcluded2, _ := c.CategorizeWithDecision(
		"README.md", []string{"fd", "How to use", "Pattern syntax"})
	want := []string{"docs.concepts"}
	if !reflect.DeepEqual(got2, want) {
		t.Errorf("got %v, want %v", got2, want)
	}
	if sectionExcluded2 {
		t.Errorf("expected sectionExcluded=false")
	}
	// Empty section_path skips section filters entirely (preamble).
	got3, _, _ := c.CategorizeWithDecision("README.md", nil)
	if !reflect.DeepEqual(got3, want) {
		t.Errorf("empty section: got %v, want %v", got3, want)
	}
}

func TestCategorize_SectionIncludesRequiresMatch(t *testing.T) {
	c, _ := New(mkRules(&categorizationpb.Category{
		DottedPath:      "docs.tutorial",
		Paths:           []string{"docs/**/*.md"},
		SectionIncludes: []string{"tutorial", "walkthrough"},
	}))
	got, _, _ := c.CategorizeWithDecision(
		"docs/x.md", []string{"Reference"})
	if len(got) != 0 {
		t.Errorf("non-matching include should drop category; got %v", got)
	}
	got2, _, _ := c.CategorizeWithDecision(
		"docs/x.md", []string{"Quick Tutorial"})
	want := []string{"docs.tutorial"}
	if !reflect.DeepEqual(got2, want) {
		t.Errorf("got %v, want %v", got2, want)
	}
}

func TestCategorize_NilSafe(t *testing.T) {
	var c *Categorizer
	got, err := c.Categorize("anything", nil)
	if err != nil || got != nil {
		t.Errorf("nil Categorizer should return (nil, nil); got (%v, %v)", got, err)
	}
}

func TestNew_RejectsEmptyDottedPath(t *testing.T) {
	_, err := New(mkRules(
		&categorizationpb.Category{Paths: []string{"*"}},
	))
	if err == nil {
		t.Errorf("expected error for empty dotted_path")
	}
}

func TestNew_RejectsNil(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Errorf("expected error for nil rules")
	}
}
