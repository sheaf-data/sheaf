package protocpp

import (
	"context"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
)

// TestExtractFreeFunctionCallRefs pins the extractor's precision: bare
// free-function call-sites are captured; macros (SCREAMING_CASE), C++ keywords,
// member calls (`.`/`->`) and qualified calls (`::`) are not.
func TestExtractFreeFunctionCallRefs(t *testing.T) {
	body := `PW_CONSTEXPR_TEST(Varint, Enc, {
  std::byte buf[8];
  EXPECT_EQ(1u, Encode(UINT32_C(0), buf));
  size_t n = EncodedSize(value);
  auto x = static_cast<uint32_t>(n);
  sb.Frobnicate();
  pw::varint::Decode(buf, &out);
  if (n > 0) { ZigZagEncode(value); }
});`
	got := map[string]bool{}
	for _, g := range extractFreeFunctionCallRefs(body) {
		got[g] = true
	}
	for _, want := range []string{"Encode", "EncodedSize", "ZigZagEncode"} {
		if !got[want] {
			t.Errorf("want %q extracted; got %v", want, got)
		}
	}
	// Macros, keywords, member calls, and qualified calls must NOT appear.
	for _, bad := range []string{
		"EXPECT_EQ", "UINT32_C", "PW_CONSTEXPR_TEST", // SCREAMING macros
		"static_cast", "if", // keywords
		"Frobnicate", // member call (sb.Frobnicate())
		"Decode",     // qualified call (pw::varint::Decode) — left to extractQualifiedTypeRefs
	} {
		if got[bad] {
			t.Errorf("%q should NOT be extracted; got %v", bad, got)
		}
	}
}

// TestDiscover_FreeFunctionCalls_OptIn proves the flag is opt-in: a Pigweed
// constexpr test that exercises a free function by bare call yields no ref by
// default, and the function name once ExtractFreeFunctionCalls is set.
func TestDiscover_FreeFunctionCalls_OptIn(t *testing.T) {
	src := `#include "pw_varint/varint.h"
using pw::varint::Encode;
using pw::varint::EncodedSize;

PW_CONSTEXPR_TEST(Varint, EncodeSmall, {
  std::byte buf[8];
  EXPECT_EQ(1u, Encode(0, buf));
  EXPECT_EQ(1u, EncodedSize(0));
});
`
	repo := setupRepo(t, map[string]string{"pw_varint/varint_test.cc": src})
	base := Config{
		Include:         []string{"**/*_test.cc"},
		ExtraTestMacros: []string{"PW_CONSTEXPR_TEST"},
		IDLPrefix:       "pw",
	}

	// OFF (default): the constexpr test is registered, but the bare calls
	// produce no contract ref.
	offTests, err := New(base).Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover (off): %v", err)
	}
	if len(offTests) != 1 {
		t.Fatalf("off: got %d tests, want 1", len(offTests))
	}
	if hasRef(offTests[0].GetContractRefs(), "Encode") {
		t.Errorf("flag OFF must not extract bare calls; got %v", offTests[0].GetContractRefs())
	}

	// ON: the bare calls become candidate refs.
	onCfg := base
	onCfg.ExtractFreeFunctionCalls = true
	onTests, err := New(onCfg).Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover (on): %v", err)
	}
	if len(onTests) != 1 {
		t.Fatalf("on: got %d tests, want 1", len(onTests))
	}
	for _, want := range []string{"Encode", "EncodedSize"} {
		if !hasRef(onTests[0].GetContractRefs(), want) {
			t.Errorf("flag ON must extract %q; got %v", want, onTests[0].GetContractRefs())
		}
	}
}
