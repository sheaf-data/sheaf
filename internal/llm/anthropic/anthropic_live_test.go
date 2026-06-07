package anthropic

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestGenerate_Live hits the real Messages API. It is skipped unless
// ANTHROPIC_API_KEY is set, so the default `go test ./...` stays offline
// and deterministic. Run it explicitly with the key in the environment
// to verify the frontier backend end to end.
func TestGenerate_Live(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set; skipping live frontier test")
	}
	c, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// Ask for a single deterministic token so the assertion is stable.
	out, err := c.Generate(ctx, `Reply with exactly the word: PONG (uppercase, nothing else).`)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(strings.ToUpper(out), "PONG") {
		t.Fatalf("unexpected response (want PONG): %q", out)
	}
	t.Logf("frontier model %s responded: %q", c.Name(), strings.TrimSpace(out))
}
