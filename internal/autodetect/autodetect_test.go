package autodetect

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTree materializes a small mixed-ecosystem fixture under a temp dir.
func writeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"api/service.proto":             "syntax = \"proto3\";\nmessage Foo {}\n",
		"include/lib/widget.h":          "#pragma once\nclass Widget { public: void Go(); };\n",
		"include/lib/macros.h":          "#define DBG(x) PW_LOG_DEBUG(x)\n#define PW_LOG_DEBUG(x) x\n",
		"src/widget_test.cc":            "#include <gtest/gtest.h>\nTEST(WidgetTest, Go) {}\n",
		"src/main.rs":                   "#[derive(Parser)]\nstruct Cli {}\nfn main() {}\n",
		"src/lib_test.rs":               "#[test]\nfn it_works() {}\n",
		"docs/guide.md":                 "# Guide\nUse the widget.\n",
		"docs/ref.rst":                  "Reference\n=========\n",
		"build/out/generated_skip.h":    "class ShouldBeSkipped {};\n", // under out/ → walker skips
		"vendor/node_modules/dep.proto": "message Skip {}\n",           // under node_modules → skipped
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDetect_MixedTree(t *testing.T) {
	root := writeTree(t)
	res, err := Detect(root, nil, nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	want := map[string]Tier{
		"proto":      TierDeterministic,
		"cppheader":  TierDeterministic,
		"llmextract": TierLLM, // headers → schemaless tail also routed to LLM
		"gtest":      TierDeterministic,
		"rust-test":  TierDeterministic,
		"clap":       TierDeterministic,
		"markdown":   TierDeterministic,
		"rst":        TierDeterministic,
	}
	got := map[string]Tier{}
	for _, d := range res.Detections {
		got[d.Adapter] = d.Tier
	}
	for adapter, tier := range want {
		gt, ok := got[adapter]
		if !ok {
			t.Errorf("expected adapter %q to be detected; got %v", adapter, keys(got))
			continue
		}
		if gt != tier {
			t.Errorf("adapter %q tier = %q, want %q", adapter, gt, tier)
		}
	}
	if !res.SchemalessHeaders {
		t.Error("SchemalessHeaders should be true when headers present")
	}
	// Build-artifact dirs must be skipped: the skipped proto/header should
	// not inflate counts (proto count is 1, not 2).
	for _, d := range res.Detections {
		if d.Adapter == "proto" && d.FileCount != 1 {
			t.Errorf("proto FileCount = %d, want 1 (node_modules proto must be skipped)", d.FileCount)
		}
	}
}

// TestDetect_SchemaNeverLLM guards the core partition: a schema-backed
// surface (proto) is deterministic; only the header tail gets the LLM.
func TestDetect_SchemaNeverLLM(t *testing.T) {
	root := writeTree(t)
	res, _ := Detect(root, nil, nil)
	for _, d := range res.Detections {
		if d.Adapter == "proto" && d.Tier == TierLLM {
			t.Error("proto must never be LLM tier")
		}
	}
}

// TestDetect_ScopedIncludeNarrowsGlobs confirms that an explicit include
// becomes the per-adapter glob (so the generated config stays narrow),
// instead of the broad derived **/*.ext.
func TestDetect_ScopedIncludeNarrowsGlobs(t *testing.T) {
	root := writeTree(t)
	res, err := Detect(root, []string{"include/lib/**/*.h"}, nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	var sawCpp bool
	for _, d := range res.Detections {
		if d.Adapter == "cppheader" {
			sawCpp = true
			if len(d.Include) != 1 || d.Include[0] != "include/lib/**/*.h" {
				t.Errorf("cppheader Include = %v, want [include/lib/**/*.h]", d.Include)
			}
		}
		// With a header-only include, no proto/markdown should be detected.
		if d.Adapter == "proto" || d.Adapter == "markdown" {
			t.Errorf("did not expect %q under a header-only include", d.Adapter)
		}
	}
	if !sawCpp {
		t.Error("cppheader not detected under scoped include")
	}
}

func keys(m map[string]Tier) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
