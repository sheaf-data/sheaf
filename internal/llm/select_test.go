package llm

import (
	"strings"
	"testing"
)

// TestResolveBackend_None confirms the deterministic-only tier and its
// aliases resolve to BackendNone, and that Disabled reports them as off.
func TestResolveBackend_None(t *testing.T) {
	for _, in := range []string{"none", "noop", "off"} {
		if got := ResolveBackendName(in); got != BackendNone {
			t.Errorf("ResolveBackendName(%q) = %q, want %q", in, got, BackendNone)
		}
		if !Disabled(in) {
			t.Errorf("Disabled(%q) = false, want true", in)
		}
	}
	if Disabled("auto") || Disabled("ollama") || Disabled("anthropic") {
		t.Error("Disabled must be false for auto/ollama/anthropic")
	}
}

// TestResolveBackend_Explicit confirms explicit backends are honored
// as-is (the none case must not have disturbed the existing mapping).
func TestResolveBackend_Explicit(t *testing.T) {
	if got := ResolveBackendName("ollama"); got != BackendOllama {
		t.Errorf("ResolveBackendName(ollama) = %q, want %q", got, BackendOllama)
	}
	if got := ResolveBackendName("anthropic"); got != BackendAnthropic {
		t.Errorf("ResolveBackendName(anthropic) = %q, want %q", got, BackendAnthropic)
	}
}

// TestNewClient_NoneErrors confirms "none" builds no client and fails
// loudly rather than falling back to a live model.
func TestNewClient_NoneErrors(t *testing.T) {
	c, err := NewClient(BackendNone, "", 0, nil)
	if err == nil {
		t.Fatal("NewClient(none) error = nil, want error")
	}
	if c != nil {
		t.Errorf("NewClient(none) client = %v, want nil", c)
	}
	if !strings.Contains(err.Error(), BackendNone) {
		t.Errorf("error %q should name the backend", err)
	}
}
