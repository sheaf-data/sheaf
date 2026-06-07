package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	configpb "github.com/sheaf-data/sheaf/proto/config"
	"github.com/sheaf-data/sheaf/utils/scanner"
	"google.golang.org/protobuf/encoding/prototext"
)

// lockedWriter serializes concurrent writes to an underlying io.Writer.
// The fan-out pools have many worker goroutines emitting per-entry log
// lines to the same destination (a *bytes.Buffer in tests, os.Stdout in
// production), neither of which is safe for concurrent use. Wrapping the
// sink in a mutex makes each Write atomic so log lines never interleave
// mid-line (and the race detector stays quiet). One Fprintf == one Write
// here because the format strings end in \n and contain no other newlines,
// so whole lines are the unit of atomicity.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func newLockedWriter(w io.Writer) *lockedWriter { return &lockedWriter{w: w} }

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// defaultManifestJobs is the bounded fan-out width used when the caller
// passes jobs <= 0. The orchestrator already parallelizes adapters within
// a single entry's scan (a goroutine per parser), so entry-level
// concurrency is deliberately HALF the CPU count (min 2, max 8) rather
// than NumCPU-wide — enough to overlap I/O-bound entries without
// oversubscribing the machine when several CPU-heavy scans land at once.
func defaultManifestJobs() int {
	j := runtime.NumCPU() / 2
	if j < 2 {
		j = 2
	}
	if j > 8 {
		j = 8
	}
	return j
}

// entrySiblingRules returns the path to a categorization-rules.textproto
// sitting next to an entry's resolved config, or "" when none exists.
// Threading this per-entry path into the render call (instead of copying
// it into a shared repoRoot/categorization-rules.textproto) is what makes
// concurrent entries independent: no two entries write the same file.
func entrySiblingRules(configPath string) string {
	sibling := filepath.Join(filepath.Dir(configPath), "categorization-rules.textproto")
	if _, err := os.Stat(sibling); err != nil {
		return ""
	}
	return sibling
}

// EntryResult is the outcome of one manifest entry's scan + render.
// Err is non-nil when the entry failed; the other count fields are
// then meaningless. The index page renders failures inline rather than
// dropping them, so a fan-out run is self-documenting.
type EntryResult struct {
	Entry        *configpb.MonorepoManifest_Entry
	OutputRel    string // entry.output, relative to outputDir — the index link target
	Group        string // entry.group — the domain bucket for the index tree
	ElementCount int
	BridgedCount int
	Stats        scanner.LibraryStats // per-surface coverage + bridge-completeness distribution
	// SurfacesRequired is the entry's declared surface set (its config's
	// surfaces_required). The index derives which per-surface bullets
	// (docs / tests / usage) to show from it — a docs-only config hides the
	// rest. Empty for older configs (the index then shows all three).
	SurfacesRequired []string
	// CacheHit is true when the entry rendered from a reused snapshot
	// (no orchestrator.Run). It drives the per-entry log line and is the
	// observable the cache test asserts on. Always false on a cold scan
	// and on failure.
	CacheHit bool
	Err      error
}

// RunManifest reads a MonorepoManifest textproto from manifestPath and
// runs scan + render for each entry. Outputs are written to outputDir
// using each Entry.output as a relative path. Writes an index.html into
// outputDir summarizing all entries.
//
// defaultRepo is the repo root applied to every entry (the value of the
// fan-out command's --repo flag); v1 has no per-entry repo override.
//
// configRoot, when non-empty, is the directory each entry's relative
// config_path resolves against (the value of --config-root). When empty,
// relative config_path values resolve against the manifest's own
// directory (back-compat). Absolute config_path values always win.
//
// Errors during a single entry's scan are collected and logged but do
// NOT abort the remaining entries (matching the per-system
// continue-on-failure semantics of regen-example-reports.sh). Returns a
// non-nil error iff at least one entry failed and failOnError was set,
// OR the manifest itself couldn't be parsed.
//
// jobs bounds how many entries scan concurrently. jobs <= 0 picks a
// machine-sized default (defaultManifestJobs); jobs == 1 reproduces the
// historical serial behavior exactly. Entry results are assigned by index
// so the index page's grouping stays in manifest order regardless of
// completion order.
//
// snapshotCacheDir, when non-empty, enables snapshot reuse: each entry's
// cold-scan Snapshot is persisted there keyed by its inputs (config + rules
// + commit + schema + library list), and a later run with unchanged inputs
// renders from the cached snapshot instead of re-scanning. An empty dir
// disables caching. forceRescan bypasses cache reads (every entry re-scans)
// but still refreshes the cache on write. See snapCache for the key's
// honest limitation (it does not cover sheaf's own adapter code).
func RunManifest(ctx context.Context, manifestPath, outputDir, defaultRepo, configRoot, baseURL string, singleFile, failOnError bool, jobs int, snapshotCacheDir string, forceRescan bool, logw io.Writer) error {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest %s: %w", manifestPath, err)
	}
	var manifest configpb.MonorepoManifest
	if err := prototext.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("parse manifest %s: %w", manifestPath, err)
	}
	if len(manifest.GetEntries()) == 0 {
		return fmt.Errorf("manifest %s has no entries", manifestPath)
	}
	manifestDir := filepath.Dir(manifestPath)

	if jobs <= 0 {
		jobs = defaultManifestJobs()
	}

	// HEAD commit is part of every entry's cache key; resolve it once for
	// the whole run rather than re-shelling out to git per entry.
	commit := gitShortCommit(defaultRepo)
	cache := newSnapCache(snapshotCacheDir, forceRescan)

	// Many worker goroutines write per-entry log lines to logw concurrently;
	// serialize them so lines never tear and the race detector stays clean.
	// (Post-wait summary writes go through the same wrapper harmlessly.)
	logw = newLockedWriter(logw)

	if singleFile {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			return fmt.Errorf("create output dir %s: %w", outputDir, err)
		}
		meta := RunMeta{
			Repo:         defaultRepo,
			Commit:       commit,
			SheafVersion: BuildVersion,
			BaseURL:      baseURL,
			GeneratedAt:  time.Now().UTC(),
		}
		return runBundle(ctx, manifest.GetEntries(), manifestDir, configRoot, outputDir, defaultRepo, commit, meta, jobs, cache, logw)
	}

	// Split layout: the index lives beside the contents dir as <root>.html;
	// every per-entry report goes in <root>-contents/ so the index page stays
	// light and each report loads on its own.
	root := filepath.Base(outputDir)
	contentsName := root + "-contents"
	contentsDir := filepath.Join(filepath.Dir(outputDir), contentsName)
	indexPath := filepath.Join(filepath.Dir(outputDir), root+".html")
	if err := os.MkdirAll(contentsDir, 0o755); err != nil {
		return fmt.Errorf("create contents dir %s: %w", contentsDir, err)
	}
	navGroups := buildNavGroups(manifest.GetEntries(), baseURL, contentsName)
	indexHref := "../" + root + ".html"
	if baseURL != "" {
		indexHref = joinBase(baseURL, root+".html")
	}
	entries := manifest.GetEntries()
	total := len(entries)

	// Fan out the entries across a bounded worker pool. Results are
	// written BY INDEX (never appended) so the index page's per-group
	// ordering is byte-stable regardless of which entry finishes first.
	// Each entry's log line is composed and emitted as it completes, so a
	// line is never torn even though the lines themselves interleave.
	//
	// The semaphore is the only width control; jobs == 1 collapses the pool
	// to a serial loop (each iteration blocks on the prior's release before
	// dispatching), reproducing the historical behavior exactly. Context
	// cancellation stops dispatching new entries: once ctx is done we record
	// a cancellation error for every not-yet-started entry and break, rather
	// than spinning up doomed scans. Entries already in flight run to
	// completion (their own ctx-aware work returns promptly).
	results := make([]EntryResult, total)
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup
	for i, entry := range entries {
		select {
		case sem <- struct{}{}:
			// Slot acquired — proceed to dispatch this entry.
		case <-ctx.Done():
			// Cancelled while waiting for a slot: mark this and every
			// remaining entry as cancelled and stop dispatching.
			for j := i; j < total; j++ {
				if results[j].Entry == nil {
					results[j] = EntryResult{
						Entry: entries[j], OutputRel: entries[j].GetOutput(),
						Group: entries[j].GetGroup(), Err: ctx.Err(),
					}
				}
			}
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int, entry *configpb.MonorepoManifest_Entry) {
			defer wg.Done()
			defer func() { <-sem }()
			nav := entryNav(navGroups, indexHref, i+1, total, entryHref(baseURL, contentsName, entry.GetOutput()), commit, entry.GetConceptDocsHref())
			res := runEntry(ctx, entry, manifestDir, configRoot, contentsDir, defaultRepo, commit, cache, nav)
			results[i] = res
			switch {
			case res.Err != nil:
				fmt.Fprintf(logw, "[%s] FAILED: %v\n", entry.GetLibrary(), res.Err)
			case res.CacheHit:
				fmt.Fprintf(logw, "[%s] %s (cached) — %d elements, %d bridged\n",
					entry.GetLibrary(), res.OutputRel, res.ElementCount, res.BridgedCount)
			default:
				fmt.Fprintf(logw, "[%s] %s — %d elements, %d bridged\n",
					entry.GetLibrary(), res.OutputRel, res.ElementCount, res.BridgedCount)
			}
		}(i, entry)
	}
	wg.Wait()

	// Count failures after the wait (race-free) by scanning the
	// index-ordered results for a recorded error.
	failures := 0
	for i := range results {
		if results[i].Err != nil {
			failures++
		}
	}

	meta := RunMeta{
		Repo:           defaultRepo,
		Commit:         commit, // resolved once above; matches the cache key's commit
		SheafVersion:   BuildVersion,
		BaseURL:        baseURL,
		GeneratedAt:    time.Now().UTC(),
		ContentsPrefix: contentsName + "/",
	}
	if err := RenderIndex(indexPath, results, meta); err != nil {
		return fmt.Errorf("render index: %w", err)
	}
	fmt.Fprintf(logw, "wrote %s (%d entries, %d failed)\n",
		indexPath, len(results), failures)

	if failOnError && failures > 0 {
		return fmt.Errorf("%d of %d manifest entries failed", failures, len(results))
	}
	return nil
}

// runBundle renders every entry to an in-memory HTML string and writes
// a single-file bundle: one index.html embedding all reports as
// hash-routed iframes. Per-report nav is omitted — the bundle's own
// sidebar is the navigation.
//
// Like the directory path, entries fan out across a bounded worker pool
// (width jobs; jobs == 1 is serial) and items are written BY INDEX so the
// bundle's sidebar order matches the manifest. The same snapshot cache is
// consulted: a cache-hit entry renders its HTML string from the reused
// snapshot with no re-scan.
func runBundle(ctx context.Context, entries []*configpb.MonorepoManifest_Entry, manifestDir, configRoot, outputDir, defaultRepo, commit string, meta RunMeta, jobs int, cache *snapCache, logw io.Writer) error {
	repoRoot := defaultRepo
	if repoRoot == "" {
		repoRoot = "."
	}
	total := len(entries)
	items := make([]bundleItem, total)
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup
	for i, entry := range entries {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			for j := i; j < total; j++ {
				if items[j].Name == "" && items[j].Err == "" {
					items[j] = bundleItem{
						Name: entries[j].GetLibrary(), Group: entries[j].GetGroup(),
						Slug: slugify(entries[j].GetOutput(), entries[j].GetLibrary()),
						Err:  ctx.Err().Error(),
					}
				}
			}
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int, entry *configpb.MonorepoManifest_Entry) {
			defer wg.Done()
			defer func() { <-sem }()
			items[i] = runBundleItem(ctx, entry, manifestDir, configRoot, repoRoot, commit, cache, logw)
		}(i, entry)
	}
	wg.Wait()

	if err := RenderBundle(outputDir, items, meta); err != nil {
		return fmt.Errorf("render bundle: %w", err)
	}
	fmt.Fprintf(logw, "wrote %s (single-file bundle, %d reports)\n", filepath.Join(outputDir, "index.html"), len(items))
	return nil
}

// runBundleItem resolves one entry, renders its report to an HTML string
// (from a reused snapshot when the cache hits, else from a cold scan that
// refreshes the cache), and returns the bundleItem. A non-empty Err field
// records a failure inline; like runEntry it never aborts the caller's pool.
func runBundleItem(ctx context.Context, entry *configpb.MonorepoManifest_Entry, manifestDir, configRoot, repoRoot, commit string, cache *snapCache, logw io.Writer) bundleItem {
	item := bundleItem{Name: entry.GetLibrary(), Group: entry.GetGroup(), Slug: slugify(entry.GetOutput(), entry.GetLibrary())}
	configPath := entry.GetConfigPath()
	if configPath == "" {
		item.Err = "empty config_path"
		fmt.Fprintf(logw, "[%s] FAILED: empty config_path\n", entry.GetLibrary())
		return item
	}
	if !filepath.IsAbs(configPath) {
		base := manifestDir
		if configRoot != "" {
			base = configRoot
		}
		configPath = filepath.Join(base, configPath)
	}
	if _, err := os.Stat(configPath); err != nil {
		item.Err = err.Error()
		fmt.Fprintf(logw, "[%s] FAILED: %v\n", entry.GetLibrary(), err)
		return item
	}

	// Per-entry sibling rules, threaded into the render call instead of
	// staged into repoRoot — concurrent entries never share a rules file.
	rulesPath := entrySiblingRules(configPath)
	sourceURLTemplate := os.Getenv("SHEAF_SOURCE_URL_TEMPLATE")

	// Cache hit: render the bundle HTML from the reused snapshot.
	if snap, _, ok := loadCachedSnapshot(cache, item.Slug, configPath, rulesPath, commit, entry.GetLibrary()); ok {
		html, st, rerr := scanner.RenderStatsStringFromSnapshot(ctx, snap, repoRoot, entry.GetEcosystem(), sourceURLTemplate, rulesPath, nil)
		if rerr != nil {
			item.Err = rerr.Error()
			fmt.Fprintf(logw, "[%s] FAILED: %v\n", entry.GetLibrary(), rerr)
			return item
		}
		item.HTML = html
		item.Stats = st
		fmt.Fprintf(logw, "[%s] bundled (cached) — %d elements, %d bridged\n", entry.GetLibrary(), st.Total, st.Bridged)
		return item
	}

	// Cache miss: cold scan, persist the snapshot, then render from it so
	// the cold and warm render paths are identical.
	snap, err := scanner.BuildSnapshot(ctx, configPath, repoRoot, entry.GetLibrary(), entry.GetLibraryLabel(), rulesPath)
	if err != nil {
		item.Err = err.Error()
		fmt.Fprintf(logw, "[%s] FAILED: %v\n", entry.GetLibrary(), err)
		return item
	}
	storeCachedSnapshot(cache, item.Slug, configPath, rulesPath, commit, entry.GetLibrary(), snap, logw)
	html, st, rerr := scanner.RenderStatsStringFromSnapshot(ctx, snap, repoRoot, entry.GetEcosystem(), sourceURLTemplate, rulesPath, nil)
	if rerr != nil {
		item.Err = rerr.Error()
		fmt.Fprintf(logw, "[%s] FAILED: %v\n", entry.GetLibrary(), rerr)
		return item
	}
	item.HTML = html
	item.Stats = st
	fmt.Fprintf(logw, "[%s] bundled — %d elements, %d bridged\n", entry.GetLibrary(), st.Total, st.Bridged)
	return item
}

// joinBase joins a relative output path onto baseURL. Empty baseURL
// yields the relative path unchanged (in-directory navigation).
func joinBase(baseURL, rel string) string {
	if baseURL == "" {
		return rel
	}
	return strings.TrimRight(baseURL, "/") + "/" + rel
}

// entryHref is a manifest entry's report link as seen from a sibling report in
// the contents dir: a bare relative output filename in the default layout, or
// an absolute baseURL/<contentsName>/<output> when publishing to a base URL.
func entryHref(baseURL, contentsName, output string) string {
	if baseURL != "" {
		return joinBase(baseURL, contentsName+"/"+output)
	}
	return output
}

// buildNavGroups builds the run-wide group→library navigation model
// (first-appearance order; ungrouped entries bucket under "Ungrouped").
// contentsName forms absolute sibling links under a base URL; relative links
// stay same-dir (a bare output filename).
func buildNavGroups(entries []*configpb.MonorepoManifest_Entry, baseURL, contentsName string) []scanner.NavGroup {
	var groups []scanner.NavGroup
	idx := map[string]int{}
	for _, e := range entries {
		g := e.GetGroup()
		if g == "" {
			g = "Ungrouped"
		}
		i, ok := idx[g]
		if !ok {
			i = len(groups)
			idx[g] = i
			groups = append(groups, scanner.NavGroup{Name: g})
		}
		groups[i].Libs = append(groups[i].Libs, scanner.NavLib{Name: e.GetLibrary(), Href: entryHref(baseURL, contentsName, e.GetOutput())})
	}
	return groups
}

// entryNav clones the run-wide groups for one report, flagging the
// current library so the switcher can pre-select it. conceptDocsHref is the
// entry's optional sibling Concept Docs report link (empty for entries with
// no concept-doc report); it rides the NavContext into reportFromSnapshot.
func entryNav(groups []scanner.NavGroup, indexHref string, pos, total int, currentHref, commit, conceptDocsHref string) *scanner.NavContext {
	cp := make([]scanner.NavGroup, len(groups))
	for i, g := range groups {
		libs := make([]scanner.NavLib, len(g.Libs))
		copy(libs, g.Libs)
		for j := range libs {
			libs[j].Current = libs[j].Href == currentHref
		}
		cp[i] = scanner.NavGroup{Name: g.Name, Libs: libs}
	}
	return &scanner.NavContext{IndexHref: indexHref, Position: pos, Total: total, Commit: commit, Groups: cp, ConceptDocsHref: conceptDocsHref}
}

// runEntry executes a single manifest entry as a get-snapshot-then-render
// pair: resolve paths and the entry's sibling categorization rules, obtain
// the library Snapshot (reused from the cache when its inputs are unchanged,
// else built by a cold scan that refreshes the cache), then render the
// report from that Snapshot. A non-nil EntryResult.Err is the recorded
// failure; runEntry never panics or aborts the caller's pool.
//
// Threading the entry's sibling rules path straight into the render call
// (rather than copying it into a shared repoRoot/categorization-rules
// file) is what makes concurrent entries independent — no two entries ever
// write the same file, eliminating the cross-contamination race the old
// stageRules approach had under parallelism.
func runEntry(ctx context.Context, entry *configpb.MonorepoManifest_Entry, manifestDir, configRoot, outputDir, defaultRepo, commit string, cache *snapCache, nav *scanner.NavContext) EntryResult {
	res := EntryResult{Entry: entry, OutputRel: entry.GetOutput(), Group: entry.GetGroup()}

	configPath := entry.GetConfigPath()
	if configPath == "" {
		res.Err = fmt.Errorf("entry has empty config_path")
		return res
	}
	// Resolution precedence: an absolute config_path wins as-is;
	// otherwise resolve against --config-root when set, else against the
	// manifest's own directory (back-compat).
	if !filepath.IsAbs(configPath) {
		base := manifestDir
		if configRoot != "" {
			base = configRoot
		}
		configPath = filepath.Join(base, configPath)
	}
	if _, err := os.Stat(configPath); err != nil {
		res.Err = fmt.Errorf("config_path %s: %w", entry.GetConfigPath(), err)
		return res
	}

	outRel := entry.GetOutput()
	if outRel == "" {
		res.Err = fmt.Errorf("entry has empty output")
		return res
	}
	outPath := outRel
	if !filepath.IsAbs(outPath) {
		outPath = filepath.Join(outputDir, outPath)
	}

	repoRoot := defaultRepo
	if repoRoot == "" {
		repoRoot = "."
	}

	// The entry's own categorization rules: a sibling
	// categorization-rules.textproto next to its config, or "" when none
	// exists (the scan then runs uncategorized — same convention as the
	// single-render path with an empty rulesPath).
	rulesPath := entrySiblingRules(configPath)
	slug := slugify(entry.GetOutput(), entry.GetLibrary())

	// Source links resolve via SHEAF_SOURCE_URL_TEMPLATE when set (a
	// {path}/{line} template for the entry's repo), giving the fan-out's
	// reports clickable source links the same way single-repo render does
	// via --source-url-template. Empty (the default) keeps bare paths.
	sourceURLTemplate := os.Getenv("SHEAF_SOURCE_URL_TEMPLATE")

	// Get-snapshot half: reuse from cache when the inputs are unchanged.
	if snap, _, ok := loadCachedSnapshot(cache, slug, configPath, rulesPath, commit, entry.GetLibrary()); ok {
		st, err := scanner.RenderStatsFromSnapshot(ctx, snap, repoRoot, entry.GetEcosystem(), sourceURLTemplate, rulesPath, outPath, nav)
		if err != nil {
			res.Err = err
			return res
		}
		res.ElementCount = st.Total
		res.BridgedCount = st.Bridged
		res.Stats = st
		res.SurfacesRequired = snap.SurfacesRequired
		res.CacheHit = true
		return res
	}

	// Cache miss: cold scan, persist the snapshot, render from it. Going
	// through RenderStatsFromSnapshot (rather than the all-in-one
	// RenderStats) keeps the cold and warm render paths byte-identical.
	snap, err := scanner.BuildSnapshot(ctx, configPath, repoRoot, entry.GetLibrary(), entry.GetLibraryLabel(), rulesPath)
	if err != nil {
		res.Err = err
		return res
	}
	storeCachedSnapshot(cache, slug, configPath, rulesPath, commit, entry.GetLibrary(), snap, nil)
	st, err := scanner.RenderStatsFromSnapshot(ctx, snap, repoRoot, entry.GetEcosystem(), sourceURLTemplate, rulesPath, outPath, nav)
	if err != nil {
		res.Err = err
		return res
	}
	res.ElementCount = st.Total
	res.BridgedCount = st.Bridged
	res.Stats = st
	res.SurfacesRequired = snap.SurfacesRequired
	return res
}

// loadCachedSnapshot computes the entry's cache key and returns its cached
// snapshot when one is valid. The key is returned alongside so the caller
// can pass it straight to storeCachedSnapshot on a miss without recomputing.
// A key-computation error (e.g. unreadable config) yields a miss; the caller
// will surface the real error from the subsequent scan.
func loadCachedSnapshot(cache *snapCache, slug, configPath, rulesPath, commit, library string) (*scanner.Snapshot, string, bool) {
	if !cache.enabled() {
		return nil, "", false
	}
	key, err := computeKey(configPath, rulesPath, commit, library)
	if err != nil {
		return nil, "", false
	}
	snap, ok := cache.Load(slug, key)
	return snap, key, ok
}

// storeCachedSnapshot recomputes the key and persists the freshly scanned
// snapshot. A failed write is logged (when logw is non-nil) but never fails
// the entry — a missing cache entry just means the next run re-scans.
func storeCachedSnapshot(cache *snapCache, slug, configPath, rulesPath, commit, library string, snap *scanner.Snapshot, logw io.Writer) {
	if !cache.enabled() {
		return
	}
	key, err := computeKey(configPath, rulesPath, commit, library)
	if err != nil {
		return
	}
	if werr := cache.Store(slug, key, snap); werr != nil && logw != nil {
		fmt.Fprintf(logw, "[%s] WARNING: snapshot cache write failed: %v\n", library, werr)
	}
}
