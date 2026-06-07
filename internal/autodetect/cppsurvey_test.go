package autodetect

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
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

func has(s []string, w string) bool {
	for _, x := range s {
		if x == w {
			return true
		}
	}
	return false
}

// TestDetect_CppSurveys covers the three C++ tuning surveys: custom
// test-declaring macros, leading class attribute macros, and Doxygen presence.
func TestDetect_CppSurveys(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"pw_x/public/pw_x/widget.h": "#pragma once\nnamespace pw {\n" +
			"class PW_LOCKABLE Widget { public: void Go(); };\n" +
			"class Plain { public: void Stop(); };\n}\n",
		"pw_x/widget_test.cc": "#include <gtest/gtest.h>\n" +
			"TEST(WidgetSuite, Go) {}\n" +
			"PW_CONSTEXPR_TEST(WidgetSuite, Const, { Go(); });\n" +
			"PW_TEST_EXPECT_EQ(actual, expected);\n", // assertion helper: lowercase args
		"docs/doxygen/Doxyfile": "GENERATE_XML = YES\n",
	})

	res, err := Detect(root, nil, nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	if !res.HasDoxygen {
		t.Error("want HasDoxygen=true (docs/doxygen/Doxyfile present)")
	}
	// Custom test-declaring macro picked up; builtin + assertion helper excluded.
	if !has(res.CppTestMacros, "PW_CONSTEXPR_TEST") {
		t.Errorf("CppTestMacros missing PW_CONSTEXPR_TEST: %v", res.CppTestMacros)
	}
	if has(res.CppTestMacros, "TEST") {
		t.Errorf("builtin TEST must not be listed: %v", res.CppTestMacros)
	}
	if has(res.CppTestMacros, "PW_TEST_EXPECT_EQ") {
		t.Errorf("assertion helper PW_TEST_EXPECT_EQ (lowercase args) must not be a test macro: %v", res.CppTestMacros)
	}
	// Leading attribute macro picked up; plain CamelCase class names are not.
	if !has(res.CppAttributeMacros, "PW_LOCKABLE") {
		t.Errorf("CppAttributeMacros missing PW_LOCKABLE: %v", res.CppAttributeMacros)
	}
	if has(res.CppAttributeMacros, "Plain") || has(res.CppAttributeMacros, "Widget") {
		t.Errorf("a normal class name must not be read as an attribute macro: %v", res.CppAttributeMacros)
	}
}

// TestDetect_NoDoxyfile keeps the negative case honest.
func TestDetect_NoDoxyfile(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"pw_x/public/pw_x/widget.h": "#pragma once\nclass Widget {};\n",
	})
	res, err := Detect(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasDoxygen {
		t.Error("HasDoxygen must be false with no Doxyfile")
	}
}
