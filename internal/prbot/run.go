// PR-bot core flow: given two pre-checked-out workspace paths,
// scan each, compute the coverage delta, render the markdown comment,
// and optionally post it via a review.Adapter.
//
// This logic is reachable from two surfaces:
//   - `sheaf review --post` CLI (one-shot, ergonomic for CI steps)
//   - MCP `review_pr` operation (programmatic, for any caller)
//
// Sheaf does not own git plumbing. Callers hand it two pre-checked-out
// directories — implementors can use GitHub Actions checkout, Gerrit
// fetcher, a custom worktree manager, or even just a `cp -r` for
// demos.

package prbot

import (
	"context"
	"errors"
	"fmt"

	"github.com/sheaf-data/sheaf/internal/corpus"
	"github.com/sheaf-data/sheaf/internal/orchestrator"
	"github.com/sheaf-data/sheaf/internal/review"
	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
	configpb "github.com/sheaf-data/sheaf/proto/config"
)

// RunOptions bundles the inputs to RunReview. Adapter is required
// when Post is true; ignored otherwise.
type RunOptions struct {
	Config   *configpb.Config
	Rules    *categorizationpb.Rules
	BaseRoot string
	HeadRoot string
	PRRef    string
	Post     bool
	Adapter  review.Adapter
}

// RunResult is what RunReview returns: the rendered comment, plus
// optional post-destination if Post was requested. BaseCorpus and
// HeadCorpus are exposed so callers can build a DeltaArtifact (for
// --emit-json) without re-running the scan.
type RunResult struct {
	Comment    *Comment
	BaseCorpus *corpus.Corpus
	HeadCorpus *corpus.Corpus
	Posted     bool
	PostedTo   string // empty when Post=false
	Adapter    string // adapter name when Posted=true; empty otherwise
}

// RunReview executes the full PR-review flow.
func RunReview(ctx context.Context, opts RunOptions) (*RunResult, error) {
	if opts.Config == nil {
		return nil, errors.New("prbot: RunOptions.Config is required")
	}
	if opts.BaseRoot == "" {
		return nil, errors.New("prbot: BaseRoot is required")
	}
	if opts.HeadRoot == "" {
		return nil, errors.New("prbot: HeadRoot is required")
	}
	if opts.Post && opts.Adapter == nil {
		return nil, errors.New("prbot: Post=true requires Adapter")
	}
	if opts.PRRef == "" {
		opts.PRRef = "PR#unknown"
	}

	baseRes, err := scan(ctx, opts.Config, opts.Rules, opts.BaseRoot)
	if err != nil {
		return nil, fmt.Errorf("scan base %s: %w", opts.BaseRoot, err)
	}
	headRes, err := scan(ctx, opts.Config, opts.Rules, opts.HeadRoot)
	if err != nil {
		return nil, fmt.Errorf("scan head %s: %w", opts.HeadRoot, err)
	}
	// Pass head findings into Render so the PR comment surfaces both
	// COVERAGE_DELTA findings (added/removed/changed) and the static
	// analyzer findings (DOCUMENTED_UNTESTED, etc.) for affected
	// elements.
	comment, err := Render(ctx, opts.PRRef, baseRes.Corpus, headRes.Corpus, opts.Rules, headRes.Findings...)
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	out := &RunResult{
		Comment:    comment,
		BaseCorpus: baseRes.Corpus,
		HeadCorpus: headRes.Corpus,
	}
	if opts.Post {
		dest, err := opts.Adapter.Post(ctx, opts.PRRef, comment.Body)
		if err != nil {
			return nil, fmt.Errorf("post via %s: %w", opts.Adapter.Name(), err)
		}
		out.Posted = true
		out.PostedTo = dest
		out.Adapter = opts.Adapter.Name()
	}
	return out, nil
}

func scan(ctx context.Context, cfg *configpb.Config, rules *categorizationpb.Rules, root string) (*orchestrator.Result, error) {
	o, err := orchestrator.New(cfg, rules, root)
	if err != nil {
		return nil, err
	}
	return o.Run(ctx)
}
