package adapters

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sheaf-data/sheaf/internal/glob"
)

// WalkMatching walks repoRoot (using os.DirFS rooted at repoRoot) and
// invokes fn for every file whose repo-relative path matches any of
// `include` and none of `exclude`. Paths passed to fn are forward-
// slash separated and relative to repoRoot.
//
// Directories named .git, target, node_modules, and out are skipped
// at any depth to avoid wasting time on build artifacts. The exclude
// patterns are still applied to whatever survives.
func WalkMatching(repoRoot string, include, exclude []string, fn func(relPath string, info fs.DirEntry) error) error {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Permission errors on a subtree: skip the subtree, don't abort.
			if os.IsPermission(walkErr) {
				return fs.SkipDir
			}
			return walkErr
		}
		// Skip well-known build artifact directories.
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "target" || name == "node_modules" || name == "out" {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		ok, err := glob.MatchAnyIncludeExclude(include, exclude, rel)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		return fn(rel, d)
	})
}

// ReadFile is a thin os.ReadFile wrapper; centralized so adapters
// can be retargeted to read from caches or virtual filesystems
// without touching their parsing code.
func ReadFile(repoRoot, relPath string) ([]byte, error) {
	return os.ReadFile(filepath.Join(repoRoot, relPath))
}

// NormalizeRelPath ensures forward-slash separators.
func NormalizeRelPath(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}
