package autoconfig

import (
	"testing"

	"github.com/sheaf-data/sheaf/internal/autodetect"
)

func contains(s []string, w string) bool {
	for _, x := range s {
		if x == w {
			return true
		}
	}
	return false
}

// TestBuild_CppTuning is the autonomous-C++ invariant: a C++ header contract
// makes --auto synthesize the tuned config the sheaf-onboard procedure would
// otherwise apply by hand — protocpp (not gtest) with the surveyed test macros
// and bare-call extraction, cppheader with the surveyed attribute macros +
// internal exclude + header-qualified include, and the doxygen reference
// surface.
func TestBuild_CppTuning(t *testing.T) {
	det := &autodetect.Result{
		SchemalessHeaders:  true,
		HasDoxygen:         true,
		CppTestMacros:      []string{"PW_CONSTEXPR_TEST"},
		CppAttributeMacros: []string{"PW_LOCKABLE"},
		Detections: []autodetect.Detection{
			{Adapter: "cppheader", Role: autodetect.RoleContract, Include: []string{"pw_x/**"}},
			{Adapter: "gtest", Role: autodetect.RoleTest, Include: []string{"pw_x/**"}},
			{Adapter: "rst", Role: autodetect.RoleDoc, Include: []string{"pw_x/**"}},
		},
	}
	cfg := Build(Options{ProjectName: "pw_x", IDLPrefix: "pw"}, det)

	// Tests: protocpp, not gtest; surveyed macro + bare-call extraction.
	if n := len(cfg.GetTestParser()); n != 1 {
		t.Fatalf("want 1 test parser, got %d", n)
	}
	pc := cfg.GetTestParser()[0].GetProtocpp()
	if pc == nil {
		t.Fatalf("want protocpp test parser, got name=%q", cfg.GetTestParser()[0].GetName())
	}
	if !pc.GetExtractFreeFunctionCalls() {
		t.Error("want extract_free_function_calls=true for a C++ header contract")
	}
	if !contains(pc.GetExtraTestMacros(), "PW_CONSTEXPR_TEST") {
		t.Errorf("extra_test_macros missing surveyed PW_CONSTEXPR_TEST: %v", pc.GetExtraTestMacros())
	}

	// Contract: cppheader, attribute macro + internal exclude + header-qualified.
	var ch = func() interface {
		GetIgnoredAttributeMacros() []string
		GetExclude() []string
		GetInclude() []string
	} {
		for _, ca := range cfg.GetContractAnchor() {
			if c := ca.GetCppHeader(); c != nil {
				return c
			}
		}
		return nil
	}()
	if ch == nil {
		t.Fatal("no cppheader anchor")
	}
	if !contains(ch.GetIgnoredAttributeMacros(), "PW_LOCKABLE") {
		t.Errorf("ignored_attribute_macros missing surveyed PW_LOCKABLE: %v", ch.GetIgnoredAttributeMacros())
	}
	if !contains(ch.GetExclude(), "**/internal/**") {
		t.Errorf("exclude missing **/internal/**: %v", ch.GetExclude())
	}
	if contains(ch.GetInclude(), "pw_x/**") {
		t.Errorf("include must be header-qualified, not the raw scope glob: %v", ch.GetInclude())
	}
	if !contains(ch.GetInclude(), "pw_x/**/*.h") {
		t.Errorf("include missing header-qualified pw_x/**/*.h: %v", ch.GetInclude())
	}

	// Docs: rst (concepts) AND doxygen (reference) coexist.
	var hasRst, hasDoxygen bool
	for _, dp := range cfg.GetDocParser() {
		if dp.GetRst() != nil {
			hasRst = true
		}
		if dp.GetDoxygen() != nil {
			hasDoxygen = true
		}
	}
	if !hasRst || !hasDoxygen {
		t.Errorf("want both rst and doxygen doc parsers; rst=%v doxygen=%v", hasRst, hasDoxygen)
	}
}

// TestBuild_NonCppUntouched proves the tuning is gated on a C++ header
// contract: a proto project keeps stock gtest and gets no doxygen even when a
// Doxyfile is present.
func TestBuild_NonCppUntouched(t *testing.T) {
	det := &autodetect.Result{
		HasDoxygen: true,
		Detections: []autodetect.Detection{
			{Adapter: "proto", Role: autodetect.RoleContract, Include: []string{"**/*.proto"}},
			{Adapter: "gtest", Role: autodetect.RoleTest, Include: []string{"**/*.cc"}},
		},
	}
	cfg := Build(Options{ProjectName: "p"}, det)
	if cfg.GetTestParser()[0].GetGtest() == nil {
		t.Error("a non-C++ contract must keep stock gtest")
	}
	for _, dp := range cfg.GetDocParser() {
		if dp.GetDoxygen() != nil {
			t.Error("doxygen must not be wired without a C++ header contract")
		}
	}
}
