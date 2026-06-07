// Package config loads and validates Sheaf project configuration.
//
// Two file types: sheaf.textproto (Config message) and
// categorization-rules.textproto (Rules message). The package handles
// parsing, environment-variable expansion in path-typed fields, and
// resolve-time validation per docs/config.md §3.3 and §7.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/encoding/prototext"

	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
	configpb "github.com/sheaf-data/sheaf/proto/config"
)

// SchemaVersion is the only Config.version this binary understands.
const SchemaVersion = 1

// ErrUnknownVersion is returned when a config declares a schema
// version other than SchemaVersion.
var ErrUnknownVersion = errors.New("config: unknown schema version")

// LoadConfig reads sheaf.textproto from path, parses it, expands env
// vars in known path fields, and runs resolve-time validation.
func LoadConfig(path string) (*configpb.Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	cfg := &configpb.Config{}
	if err := prototext.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if cfg.GetVersion() != SchemaVersion {
		return nil, fmt.Errorf("%w: file declares version %d, this binary expects %d",
			ErrUnknownVersion, cfg.GetVersion(), SchemaVersion)
	}
	ExpandEnvInPlace(cfg)
	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("config: validate %s: %w", path, err)
	}
	return cfg, nil
}

// LoadRules reads categorization-rules.textproto from path.
func LoadRules(path string) (*categorizationpb.Rules, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("rules: read %s: %w", path, err)
	}
	rules := &categorizationpb.Rules{}
	if err := prototext.Unmarshal(raw, rules); err != nil {
		return nil, fmt.Errorf("rules: parse %s: %w", path, err)
	}
	if rules.GetVersion() != SchemaVersion {
		return nil, fmt.Errorf("%w: rules file declares version %d, this binary expects %d",
			ErrUnknownVersion, rules.GetVersion(), SchemaVersion)
	}
	if err := ValidateRules(rules); err != nil {
		return nil, fmt.Errorf("rules: validate %s: %w", path, err)
	}
	return rules, nil
}

// ExpandEnvInPlace walks the Config replacing ${VAR} in known path
// fields with their environment values. Unset variables expand to ""
// (a warning at runtime; here we just substitute).
func ExpandEnvInPlace(c *configpb.Config) {
	for _, anchor := range c.GetContractAnchor() {
		if f := anchor.GetFidl(); f != nil {
			f.FidlcPath = expand(f.FidlcPath)
			f.Include = expandAll(f.Include)
			f.Exclude = expandAll(f.Exclude)
			f.PrebuiltIrDir = expand(f.PrebuiltIrDir)
		}
		if a := anchor.GetArgh(); a != nil {
			a.CrateRoots = expandAll(a.CrateRoots)
			a.Include = expandAll(a.Include)
			a.Exclude = expandAll(a.Exclude)
		}
		if c := anchor.GetClap(); c != nil {
			c.CrateRoots = expandAll(c.CrateRoots)
			c.Include = expandAll(c.Include)
			c.Exclude = expandAll(c.Exclude)
		}
		if c := anchor.GetCobra(); c != nil {
			c.YamlDir = expand(c.YamlDir)
			c.Include = expandAll(c.Include)
			c.Exclude = expandAll(c.Exclude)
		}
	}
	for _, rr := range c.GetRenderedReference() {
		if f := rr.GetFidldoc(); f != nil {
			f.BundlePath = expand(f.BundlePath)
		}
		if cd := rr.GetClidoc(); cd != nil {
			cd.BundlePath = expand(cd.BundlePath)
		}
		if m := rr.GetMarkdowncli(); m != nil {
			m.DocsDir = expand(m.DocsDir)
			m.Include = expandAll(m.Include)
			m.Exclude = expandAll(m.Exclude)
			m.BinaryName = expand(m.BinaryName)
		}
	}
	for _, tp := range c.GetTestParser() {
		if g := tp.GetGtest(); g != nil {
			g.Include = expandAll(g.Include)
			g.Exclude = expandAll(g.Exclude)
		}
		if r := tp.GetRustTest(); r != nil {
			r.Include = expandAll(r.Include)
			r.Exclude = expandAll(r.Exclude)
		}
		if b := tp.GetBats(); b != nil {
			b.Include = expandAll(b.Include)
			b.Exclude = expandAll(b.Exclude)
		}
		if g := tp.GetGotest(); g != nil {
			g.Include = expandAll(g.Include)
			g.Exclude = expandAll(g.Exclude)
			g.BinaryName = expand(g.BinaryName)
		}
		if pt := tp.GetPythonTest(); pt != nil {
			pt.Include = expandAll(pt.Include)
			pt.Exclude = expandAll(pt.Exclude)
		}
	}
	for _, dp := range c.GetDocParser() {
		if m := dp.GetMarkdown(); m != nil {
			m.Include = expandAll(m.Include)
			m.Exclude = expandAll(m.Exclude)
		}
	}
	for _, im := range c.GetImplementsMap() {
		im.Include = expandAll(im.Include)
		im.Exclude = expandAll(im.Exclude)
	}
	if fs := c.GetCache().GetFilesystem(); fs != nil {
		fs.Path = expand(fs.Path)
	}
	if g := c.GetVcs().GetGit(); g != nil {
		g.Root = expand(g.Root)
	}
}

func expand(s string) string {
	if s == "" {
		return s
	}
	// Support ~ as $HOME.
	if strings.HasPrefix(s, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			s = filepath.Join(home, s[2:])
		}
	}
	return os.ExpandEnv(s)
}

func expandAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = expand(s)
	}
	return out
}
