package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// File is a review adapter that writes the comment to a file. Useful
// for demos, local development, and CI environments that want the
// comment as an artifact instead of a posted review.
//
// If OutputDir is set, the comment is written as a separate file per
// PR (slugified pr_ref + ".md"). Otherwise OutputPath is overwritten
// each time. One of the two must be set.
type File struct {
	OutputDir  string
	OutputPath string
}

// NewFile constructs a File adapter writing to outputPath (single
// file, overwritten each post).
func NewFile(outputPath string) *File {
	return &File{OutputPath: outputPath}
}

// NewFileDir constructs a File adapter writing one file per PR under outputDir.
func NewFileDir(outputDir string) *File {
	return &File{OutputDir: outputDir}
}

// NewFileFromEnv reads SHEAF_REVIEW_FILE_OUT to decide where to write.
// If the env var points at a directory (or doesn't exist as a regular
// file), uses NewFileDir; otherwise NewFile. Defaults to current
// directory's `./sheaf-review-comments/` if the env var is unset.
func NewFileFromEnv() *File {
	out := os.Getenv("SHEAF_REVIEW_FILE_OUT")
	if out == "" {
		out = "./sheaf-review-comments"
		return NewFileDir(out)
	}
	st, err := os.Stat(out)
	if err == nil && st.IsDir() {
		return NewFileDir(out)
	}
	if strings.HasSuffix(out, "/") || strings.HasSuffix(out, string(os.PathSeparator)) {
		return NewFileDir(out)
	}
	return NewFile(out)
}

func (f *File) Name() string { return "file" }

func (f *File) Post(_ context.Context, prRef, body string) (string, error) {
	if f.OutputDir == "" && f.OutputPath == "" {
		return "", fmt.Errorf("file: neither OutputDir nor OutputPath set")
	}
	var path string
	if f.OutputDir != "" {
		if err := os.MkdirAll(f.OutputDir, 0o755); err != nil {
			return "", err
		}
		path = filepath.Join(f.OutputDir, slugifyForFile(prRef)+".md")
	} else {
		path = f.OutputPath
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(path)
	return "file://" + abs, nil
}

// slugifyForFile makes a PR reference safe for a filename.
// "PR#4521" → "PR-4521"; "github:owner/repo#42" → "github-owner-repo-42"
func slugifyForFile(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSep := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevSep = false
		default:
			if !prevSep {
				b.WriteByte('-')
				prevSep = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		return "review"
	}
	return out
}
