package analyze

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sheaf-data/sheaf/internal/corpus"
	"github.com/sheaf-data/sheaf/internal/coverage/refs"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

func init() {
	Register("stale-doc", func(name string) Analyzer {
		return &staleDoc{name: name}
	})
}

// staleDoc fires when an element's reference documentation looks old
// relative to its source. v1 uses filesystem mtime as a proxy for
// "when was this last touched". A future v2+ pass would consult git
// blame for precise dating.
//
// Threshold default: 90 days. Override via `max_age_days` Kv (int).
type staleDoc struct {
	name string
}

func (a *staleDoc) Name() string { return a.name }
func (a *staleDoc) Description() string {
	return "reference doc last touched well before its implementation"
}

func (a *staleDoc) Analyze(_ context.Context, c *corpus.Corpus, opts Options) ([]*findingpb.Finding, error) {
	// repo_root from Kv — required for resolving paths to absolute.
	root, _ := opts.Kv["repo_root"].(string)
	if root == "" {
		// Without a repo_root, we can't stat files; analyzer is a no-op.
		return nil, nil
	}
	maxLagDays := int64(90)
	if v, ok := opts.Kv["max_lag_days"].(int64); ok {
		maxLagDays = v
	}
	var out []*findingpb.Finding
	for _, e := range c.Elements() {
		if SuppressedByPath(e.GetLocation().GetPath(), opts.SuppressForPaths) {
			continue
		}
		implPath := e.GetLocation().GetPath()
		if implPath == "" {
			continue
		}
		p := c.Profile(e.GetId())
		if p == nil || p.GetDocs() == nil || p.GetDocs().GetReference() == nil {
			continue
		}
		docPath := refs.FirstReferencePath(p.GetDocs().GetReference())
		if docPath == "" || docPath == implPath {
			// When the doc IS the source (e.g., inline `///` comments),
			// staleness is intrinsic to the source — not a finding.
			continue
		}
		implMtime, err := mtime(root, implPath)
		if err != nil {
			continue
		}
		docMtime, err := mtime(root, docPath)
		if err != nil {
			continue
		}
		lagDays := (implMtime - docMtime) / 86400
		if lagDays < maxLagDays {
			continue
		}
		out = append(out, &findingpb.Finding{
			Id:       FindingID(a.name, "stale", e.GetId()),
			Kind:     findingpb.FindingKind_STALE_DOC,
			Subject:  e.GetId(),
			Severity: opts.Severity,
			Analyzer: a.name,
			Message:  fmt.Sprintf("doc last touched %d days before impl (threshold %d)", lagDays, maxLagDays),
		})
	}
	return out, nil
}

func mtime(root, rel string) (int64, error) {
	st, err := os.Stat(filepath.Join(root, rel))
	if err != nil {
		return 0, err
	}
	return st.ModTime().Unix(), nil
}
