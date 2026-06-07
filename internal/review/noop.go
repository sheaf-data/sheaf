package review

import (
	"context"
)

// Noop is the default adapter — it doesn't post anything. Used for
// dry-runs and test/CI environments where we want to render the
// comment without side effects.
type Noop struct{}

func (Noop) Name() string { return "noop" }

func (Noop) Post(_ context.Context, prRef, _ string) (string, error) {
	return "noop: would post to " + prRef, nil
}
