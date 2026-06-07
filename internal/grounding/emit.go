package grounding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Emit writes the Report as pretty-printed JSON to w. The output validates
// against docs/grounding/grounding.schema.json and is key-for-key identical
// in shape to docs/grounding/grounding.fixture.json. A trailing newline is
// written so the file is POSIX-clean.
//
// HTML escaping is disabled so the “smart quotes” and ● glyphs in blurbs/
// fix copy round-trip as UTF-8 rather than \u escapes — the fixture stores
// them literally.
func Emit(w io.Writer, rep *Report) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rep); err != nil {
		return fmt.Errorf("grounding.Emit: marshal: %w", err)
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("grounding.Emit: write: %w", err)
	}
	return nil
}

// EmitFile writes the Report JSON to path, creating parent directories as
// needed. Used by the CLI's --out.
func EmitFile(path string, rep *Report) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("grounding.EmitFile: mkdir %s: %w", dir, err)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("grounding.EmitFile: create %s: %w", path, err)
	}
	defer f.Close()
	return Emit(f, rep)
}
