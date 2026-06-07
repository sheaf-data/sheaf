package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sheaf-data/sheaf/internal/librarysnapshot"
	"github.com/sheaf-data/sheaf/utils/scanner"
)

// snapCache is the per-run snapshot reuse cache for the manifest fan-out.
// Each entry's expensive product — the merged library *Snapshot from a
// cold scan — is persisted to a slug-named JSON file alongside a .key
// sidecar recording the hash of the inputs that produced it. A warm run
// recomputes the key and reuses the snapshot only when it matches, turning
// the get-snapshot half of the entry into a cheap file read so the render
// half (which always runs) picks up template/UI changes for free.
//
// HONEST LIMITATION: the key covers the entry's INPUTS — config bytes,
// sibling categorization-rules bytes, repo HEAD commit, the librarysnapshot
// schema version, and the entry's library list — NOT sheaf's own adapter /
// scanner code. So after changing scanner or adapter logic you must pass
// --force-rescan (or delete the cache dir) to refresh; the cache cannot see
// that the producing code changed. Config changes (e.g. later wiring in
// test/impl bridges) DO change the key and auto-invalidate. Template/UI
// changes do NOT change the key and are correctly picked up regardless
// because the render step is never cached.
type snapCache struct {
	dir   string // cache directory; "" disables the cache entirely
	force bool   // --force-rescan: bypass reads, but still refresh on write
}

// newSnapCache returns a cache rooted at dir. A "" dir yields a disabled
// cache (every Load misses, every Store is a no-op) so the caller's
// cold-path code is identical whether or not caching is on.
func newSnapCache(dir string, force bool) *snapCache {
	return &snapCache{dir: strings.TrimSpace(dir), force: force}
}

func (c *snapCache) enabled() bool { return c != nil && c.dir != "" }

// computeKey hashes the entry's inputs into a stable hex digest. It reads
// the config and, when present, the sibling categorization-rules file, and
// folds in the repo HEAD commit, the schema version, and the library list.
// A read error on the config is surfaced (a missing config is a genuine
// entry failure the caller already reports); a missing sibling rules file
// is simply absent from the hash (matching the uncategorized scan path).
func computeKey(configPath, rulesPath, commit, library string) (string, error) {
	h := sha256.New()
	// Domain-separate each field with a length-prefixed tag so no two
	// different input tuples can collide by concatenation.
	writeField := func(tag string, b []byte) {
		fmt.Fprintf(h, "%s:%d:", tag, len(b))
		h.Write(b)
	}

	cfgBytes, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("snapshot cache: read config %s: %w", configPath, err)
	}
	writeField("config", cfgBytes)

	if rulesPath != "" {
		if rb, rerr := os.ReadFile(rulesPath); rerr == nil {
			writeField("rules", rb)
		} else {
			// A declared-but-unreadable rules path is a meaningful input
			// difference; fold the error text so it doesn't silently alias
			// the no-rules case.
			writeField("rules-err", []byte(rerr.Error()))
		}
	} else {
		writeField("rules", nil)
	}

	writeField("commit", []byte(commit))
	writeField("schema", []byte(fmt.Sprintf("%d", librarysnapshot.SchemaVersion)))
	writeField("library", []byte(library))

	return hex.EncodeToString(h.Sum(nil)), nil
}

func (c *snapCache) snapPath(slug string) string { return filepath.Join(c.dir, slug+".snap.json") }
func (c *snapCache) keyPath(slug string) string  { return filepath.Join(c.dir, slug+".key") }

// Load returns the cached snapshot for slug iff the cache is enabled, not
// in force-rescan mode, the stored key matches wantKey, and the snapshot
// JSON parses with the current schema version. Any miss (disabled, forced,
// key mismatch, absent/corrupt file, schema drift) returns (nil, false)
// so the caller falls through to a cold scan. Errors are intentionally
// swallowed into a miss: a bad cache must never fail a run, only re-scan.
func (c *snapCache) Load(slug, wantKey string) (*scanner.Snapshot, bool) {
	if !c.enabled() || c.force {
		return nil, false
	}
	storedKey, err := os.ReadFile(c.keyPath(slug))
	if err != nil || strings.TrimSpace(string(storedKey)) != wantKey {
		return nil, false
	}
	data, err := os.ReadFile(c.snapPath(slug))
	if err != nil {
		return nil, false
	}
	var snap scanner.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, false
	}
	// A snapshot written under a different schema must not be replayed: the
	// projection shape would differ. The key already folds the schema
	// version, so this is belt-and-suspenders against a hand-edited sidecar.
	if snap.SchemaVersion != 0 && snap.SchemaVersion != librarysnapshot.SchemaVersion {
		return nil, false
	}
	return &snap, true
}

// Store persists snap under slug and writes the key sidecar. It runs after
// a cold scan whether or not force-rescan was set (so a forced run refreshes
// the cache for the next warm run). A disabled cache is a no-op. Write
// errors are returned so the caller can log them, but they are non-fatal to
// the run — a failed cache write just means the next run re-scans.
func (c *snapCache) Store(slug, key string, snap *scanner.Snapshot) error {
	if !c.enabled() || snap == nil {
		return nil
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return fmt.Errorf("snapshot cache: mkdir %s: %w", c.dir, err)
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("snapshot cache: marshal %s: %w", slug, err)
	}
	if err := os.WriteFile(c.snapPath(slug), data, 0o644); err != nil {
		return fmt.Errorf("snapshot cache: write %s: %w", c.snapPath(slug), err)
	}
	if err := os.WriteFile(c.keyPath(slug), []byte(key), 0o644); err != nil {
		return fmt.Errorf("snapshot cache: write %s: %w", c.keyPath(slug), err)
	}
	return nil
}
