package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// countCached returns how many per-entry log lines reported a cache hit
// (the "(cached)" marker runEntry/runBundleItem print only when an entry
// rendered from a reused snapshot with no orchestrator.Run).
func countCached(log string) int {
	n := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "(cached)") {
			n++
		}
	}
	return n
}

// TestRunManifest_SnapshotCache_ReuseAndInvalidate covers the snapshot
// reuse lever end to end:
//   - a cold run populates the cache and scans every entry (no cache hits);
//   - a warm run with unchanged inputs reuses BOTH snapshots (no scan);
//   - editing one entry's config invalidates only that entry (it re-scans,
//     the untouched entry still hits);
//   - --force-rescan bypasses the cache entirely (no hits) while still
//     refreshing it.
//
// The cache-hit count is the sentinel: a hit means the entry skipped
// orchestrator.Run and rendered from its persisted Snapshot.
func TestRunManifest_SnapshotCache_ReuseAndInvalidate(t *testing.T) {
	manifestPath, repoRoot := fanoutFixture(t) // 2 entries: toola, toolb
	outDir := t.TempDir()
	cacheDir := filepath.Join(outDir, ".snapshot-cache")

	run := func(force bool) string {
		t.Helper()
		var log bytes.Buffer
		// jobs=2 so both the parallel pool and the cache compose; the
		// outcome (which entries scan vs reuse) must be deterministic
		// regardless of order.
		if err := RunManifest(context.Background(), manifestPath, outDir, repoRoot, "", "", false, false, 2, cacheDir, force, &log); err != nil {
			t.Fatalf("RunManifest: %v\nlog:\n%s", err, log.String())
		}
		return log.String()
	}

	// 1) COLD run: nothing cached yet → both entries scan, zero hits.
	cold := run(false)
	if got := countCached(cold); got != 0 {
		t.Errorf("cold run: expected 0 cache hits, got %d\nlog:\n%s", got, cold)
	}
	// The cache must now hold a snapshot + key sidecar for each entry.
	for _, slug := range []string{"toola", "toolb"} {
		for _, suffix := range []string{".snap.json", ".key"} {
			if _, err := os.Stat(filepath.Join(cacheDir, slug+suffix)); err != nil {
				t.Errorf("cold run did not persist %s%s: %v", slug, suffix, err)
			}
		}
	}
	// Reports must still be produced on the cold run.
	indexPath, contentsDir := splitOut(outDir)
	for _, name := range []string{"toola.html", "toolb.html"} {
		if _, err := os.Stat(filepath.Join(contentsDir, name)); err != nil {
			t.Errorf("cold run missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(indexPath); err != nil {
		t.Errorf("cold run missing index: %v", err)
	}

	// 2) WARM run, inputs unchanged: BOTH entries reuse their snapshot.
	warm := run(false)
	if got := countCached(warm); got != 2 {
		t.Errorf("warm run: expected 2 cache hits (no re-scan), got %d\nlog:\n%s", got, warm)
	}

	// 3) Mutate toola's config (append a harmless comment). Its cache key
	// folds the config bytes, so toola must invalidate and re-scan while
	// toolb (untouched) still hits.
	toolaCfg := filepath.Join(filepath.Dir(manifestPath), "cfgA", "sheaf.textproto")
	data, err := os.ReadFile(toolaCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(toolaCfg, append(data, []byte("\n# cache-buster comment\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	afterEdit := run(false)
	if strings.Contains(afterEdit, "[toola] toola.html (cached)") {
		t.Errorf("after editing toola's config, toola should re-scan, not hit cache\nlog:\n%s", afterEdit)
	}
	if !strings.Contains(afterEdit, "[toolb] toolb.html (cached)") {
		t.Errorf("toolb was untouched and should still hit cache\nlog:\n%s", afterEdit)
	}
	if got := countCached(afterEdit); got != 1 {
		t.Errorf("after editing one config: expected exactly 1 cache hit (toolb), got %d\nlog:\n%s", got, afterEdit)
	}

	// 4) After the edit re-populated toola's cache, a plain warm run hits
	//    both again — proving the invalidation refreshed the entry.
	warm2 := run(false)
	if got := countCached(warm2); got != 2 {
		t.Errorf("warm run after refresh: expected 2 cache hits, got %d\nlog:\n%s", got, warm2)
	}

	// 5) --force-rescan: bypass the cache for every entry (zero hits).
	forced := run(true)
	if got := countCached(forced); got != 0 {
		t.Errorf("--force-rescan: expected 0 cache hits, got %d\nlog:\n%s", got, forced)
	}
	// …but it still refreshes the cache, so the next warm run hits again.
	warm3 := run(false)
	if got := countCached(warm3); got != 2 {
		t.Errorf("warm run after --force-rescan: expected 2 cache hits, got %d\nlog:\n%s", got, warm3)
	}
}

// TestRunManifest_Bundle_CacheAndParallel exercises the single-file bundle
// path under jobs>1 + caching: a cold run scans every entry, a warm run
// reuses both snapshots, and the bundle still renders. This guards the
// runBundleItem cache branch the directory-path tests don't reach.
func TestRunManifest_Bundle_CacheAndParallel(t *testing.T) {
	manifestPath, repoRoot := fanoutFixture(t)
	outDir := t.TempDir()
	cacheDir := filepath.Join(outDir, ".snapshot-cache")
	run := func() string {
		t.Helper()
		var log bytes.Buffer
		// singleFile=true engages runBundle.
		if err := RunManifest(context.Background(), manifestPath, outDir, repoRoot, "", "", true, false, 2, cacheDir, false, &log); err != nil {
			t.Fatalf("RunManifest bundle: %v\nlog:\n%s", err, log.String())
		}
		return log.String()
	}
	cold := run()
	if got := countCached(cold); got != 0 {
		t.Errorf("cold bundle run: expected 0 cache hits, got %d\nlog:\n%s", got, cold)
	}
	if _, err := os.Stat(filepath.Join(outDir, "index.html")); err != nil {
		t.Errorf("bundle index.html not written: %v", err)
	}
	warm := run()
	if got := countCached(warm); got != 2 {
		t.Errorf("warm bundle run: expected 2 cache hits, got %d\nlog:\n%s", got, warm)
	}
}

// TestRunManifest_NoCacheDir_AlwaysScans confirms that with caching
// disabled (empty cache dir) no entry ever reports a cache hit and no
// cache directory is created — the historical behavior is preserved when
// the lever is off.
func TestRunManifest_NoCacheDir_AlwaysScans(t *testing.T) {
	manifestPath, repoRoot := fanoutFixture(t)
	outDir := t.TempDir()
	var log bytes.Buffer
	if err := RunManifest(context.Background(), manifestPath, outDir, repoRoot, "", "", false, false, 2, "", false, &log); err != nil {
		t.Fatalf("RunManifest: %v\nlog:\n%s", err, log.String())
	}
	if got := countCached(log.String()); got != 0 {
		t.Errorf("caching disabled: expected 0 cache hits, got %d", got)
	}
	if _, err := os.Stat(filepath.Join(outDir, ".snapshot-cache")); err == nil {
		t.Errorf("caching disabled: no .snapshot-cache dir should be created")
	}
}
