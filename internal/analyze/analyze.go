// Package analyze defines the Analyzer interface and runs configured
// analyzers over a corpus of CoverageProfiles.
//
// Each Analyzer reads the corpus and emits Findings. The package is
// pluggable: concrete analyzers register via init() into a global
// registry that the orchestrator consults at construction time.

package analyze

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/sheaf-data/sheaf/internal/corpus"
	"github.com/sheaf-data/sheaf/internal/glob"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	configpb "github.com/sheaf-data/sheaf/proto/config"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

// Analyzer is the pluggable surface for gap detection. Each
// implementation reads the corpus and produces zero or more findings.
type Analyzer interface {
	Name() string
	Description() string
	Analyze(ctx context.Context, corpus *corpus.Corpus, opts Options) ([]*findingpb.Finding, error)
}

// Options carries per-analyzer configuration from AnalyzerConfig.
// Severity is the user-declared severity (overrides the default).
// SuppressForPaths lets the user mute findings on certain source paths.
// Kv is a free-form map for analyzer-specific knobs.
type Options struct {
	Severity         commonpb.Severity
	SuppressForPaths []string
	Kv               map[string]any
}

// Factory constructs an Analyzer from its name + opts. Adapters
// register their factory at init() time.
type Factory func(name string) Analyzer

var (
	registryMu sync.RWMutex
	registry   = make(map[string]Factory)
)

// Register associates a factory with an analyzer name.
func Register(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = f
}

// Lookup returns the factory registered under name (nil if absent).
func Lookup(name string) Factory {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[name]
}

// RegisteredNames returns the sorted list of registered analyzer names.
func RegisteredNames() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ResolveOptions translates a configpb.AnalyzerConfig into Options.
func ResolveOptions(cfg *configpb.AnalyzerConfig) Options {
	opts := Options{
		Severity:         severityFromConfig(cfg.GetSeverity()),
		SuppressForPaths: cfg.GetSuppressForPaths(),
		Kv:               make(map[string]any),
	}
	for _, kv := range cfg.GetConfig() {
		switch v := kv.GetValue().(type) {
		case *configpb.AnalyzerKV_StringValue:
			// Multi-value: store both as a single slice when keys repeat.
			if prior, ok := opts.Kv[kv.GetKey()]; ok {
				switch existing := prior.(type) {
				case []string:
					opts.Kv[kv.GetKey()] = append(existing, v.StringValue)
				case string:
					opts.Kv[kv.GetKey()] = []string{existing, v.StringValue}
				}
			} else {
				opts.Kv[kv.GetKey()] = v.StringValue
			}
		case *configpb.AnalyzerKV_IntValue:
			opts.Kv[kv.GetKey()] = v.IntValue
		case *configpb.AnalyzerKV_BoolValue:
			opts.Kv[kv.GetKey()] = v.BoolValue
		}
	}
	return opts
}

func severityFromConfig(s configpb.Severity) commonpb.Severity {
	switch s {
	case configpb.Severity_INFO:
		return commonpb.Severity_INFO
	case configpb.Severity_WARNING:
		return commonpb.Severity_WARNING
	case configpb.Severity_ERROR:
		return commonpb.Severity_ERROR
	default:
		return commonpb.Severity_WARNING
	}
}

// RunAll runs every analyzer specified in cfg and concatenates their
// findings. Findings are sorted by (subject, kind, severity) for
// determinism. Per-analyzer errors propagate.
func RunAll(ctx context.Context, c *corpus.Corpus, cfg *configpb.Config) ([]*findingpb.Finding, error) {
	var all []*findingpb.Finding
	for _, aCfg := range cfg.GetAnalyzer() {
		factory := Lookup(aCfg.GetName())
		if factory == nil {
			return nil, fmt.Errorf("analyze: no such analyzer %q", aCfg.GetName())
		}
		analyzer := factory(aCfg.GetName())
		findings, err := analyzer.Analyze(ctx, c, ResolveOptions(aCfg))
		if err != nil {
			return nil, fmt.Errorf("analyze: %s: %w", aCfg.GetName(), err)
		}
		all = append(all, findings...)
	}
	SortFindings(all)
	return all, nil
}

// SortFindings is stable + deterministic: subject, then kind, then severity.
func SortFindings(f []*findingpb.Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].GetSubject() != f[j].GetSubject() {
			return f[i].GetSubject() < f[j].GetSubject()
		}
		if f[i].GetKind() != f[j].GetKind() {
			return f[i].GetKind() < f[j].GetKind()
		}
		return f[i].GetSeverity() > f[j].GetSeverity()
	})
}

// SuppressedByPath returns true if any pattern matches path.
func SuppressedByPath(path string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	ok, _ := glob.MatchAny(patterns, path)
	return ok
}

// FindingID derives a stable identifier from the constituent fields.
func FindingID(analyzer, kind, subject string) string {
	return analyzer + ":" + strings.ToLower(kind) + ":" + subject
}

// Severity / kind helpers used across analyzers.

func KindString(k findingpb.FindingKind) string {
	return strings.TrimPrefix(k.String(), "FINDING_KIND_")
}

func SeverityName(s commonpb.Severity) string {
	switch s {
	case commonpb.Severity_INFO:
		return "INFO"
	case commonpb.Severity_WARNING:
		return "WARNING"
	case commonpb.Severity_ERROR:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}
