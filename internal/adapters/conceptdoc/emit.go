package conceptdoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/encoding/protojson"

	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

// SchemaVersion is the on-disk version of the emitted docs.concepts report.
const SchemaVersion = 1

// Emit writes the Result as pretty-printed JSON to w. The envelope is a
// stable, reviewable shape: a summary, the per-element coverage rollup, and
// the flat anchored-claim list. DocClaims are rendered with protojson
// (snake_case wire shape, consistent with the rest of sheaf). A trailing
// newline keeps the file POSIX-clean. Deterministic for identical input.
func Emit(w io.Writer, res *Result) error {
	// Render claims via protojson so the wire shape matches sheaf's other
	// proto JSON, then re-decode into json.RawMessage so they nest inside
	// the plain-Go envelope without re-escaping.
	mo := protojson.MarshalOptions{Indent: "  ", UseProtoNames: true}

	type elemView struct {
		ElementID  string            `json:"element_id"`
		Display    string            `json:"display"`
		Kind       string            `json:"kind"`
		Covered    bool              `json:"covered"`
		ClaimCount int               `json:"claim_count"`
		DocPaths   []string          `json:"doc_paths"`
		Claims     []json.RawMessage `json:"claims"`
	}
	type envelope struct {
		SchemaVersion   int               `json:"schema_version"`
		Surface         string            `json:"surface"`
		Library         string            `json:"library"`
		LibraryDisplay  string            `json:"library_display"`
		ElementsTotal   int               `json:"elements_total"`
		ElementsCovered int               `json:"elements_covered"`
		ElementsPct     int               `json:"elements_pct"`
		ClaimsTotal     int               `json:"claims_total"`
		DocsScanned     int               `json:"docs_scanned"`
		Elements        []elemView        `json:"elements"`
		Claims          []json.RawMessage `json:"claims"`
	}

	rawClaim := func(c *docclaimpb.DocClaim) (json.RawMessage, error) {
		b, err := mo.Marshal(c)
		if err != nil {
			return nil, err
		}
		// protojson is not byte-stable across runs (it injects random
		// whitespace); compact normalizes it so output is deterministic.
		var compact bytes.Buffer
		if err := json.Compact(&compact, b); err != nil {
			return nil, err
		}
		return json.RawMessage(compact.Bytes()), nil
	}

	env := envelope{
		SchemaVersion:   SchemaVersion,
		Surface:         "docs.concepts",
		Library:         res.Summary.Library,
		LibraryDisplay:  res.Summary.LibraryDisplay,
		ElementsTotal:   res.Summary.ElementsTotal,
		ElementsCovered: res.Summary.ElementsCovered,
		ElementsPct:     res.Summary.ElementsPct,
		ClaimsTotal:     res.Summary.ClaimsTotal,
		DocsScanned:     res.Summary.DocsScanned,
	}
	for i := range res.Elements {
		e := &res.Elements[i]
		ev := elemView{
			ElementID:  e.ElementID,
			Display:    e.Display,
			Kind:       e.Kind,
			Covered:    e.Covered,
			ClaimCount: e.ClaimCount,
			DocPaths:   e.DocPaths,
			Claims:     make([]json.RawMessage, 0, len(e.Claims)),
		}
		for _, c := range e.Claims {
			rm, err := rawClaim(c)
			if err != nil {
				return fmt.Errorf("conceptdoc.Emit: marshal claim: %w", err)
			}
			ev.Claims = append(ev.Claims, rm)
		}
		env.Elements = append(env.Elements, ev)
	}
	env.Claims = make([]json.RawMessage, 0, len(res.Claims))
	for _, c := range res.Claims {
		rm, err := rawClaim(c)
		if err != nil {
			return fmt.Errorf("conceptdoc.Emit: marshal claim: %w", err)
		}
		env.Claims = append(env.Claims, rm)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		return fmt.Errorf("conceptdoc.Emit: marshal: %w", err)
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("conceptdoc.Emit: write: %w", err)
	}
	return nil
}

// EmitFile writes the Result JSON to path, creating parent dirs as needed.
func EmitFile(path string, res *Result) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("conceptdoc.EmitFile: mkdir %s: %w", dir, err)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("conceptdoc.EmitFile: create %s: %w", path, err)
	}
	defer f.Close()
	return Emit(f, res)
}
