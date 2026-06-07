package prbot

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
)

func TestBuildAndMarshalDelta_DeterministicAndRoundTrips(t *testing.T) {
	base, head := mkPair()
	rules := &categorizationpb.Rules{Version: 1}
	c, err := Render(context.Background(), "example/lib@aaa1111", base, head, rules)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	in := DeltaInputs{
		System:         "lib",
		Config:         "docs/examples/lib-coverage-config.textproto",
		BaseRef:        "0123456789abcdef",
		HeadRef:        "fedcba9876543210",
		BaseShort:      "0123456",
		HeadShort:      "fedcba9",
		ScanID:         "sheaf-lib-fedcba9",
		ScannedAt:      time.Date(2026, 5, 26, 18, 14, 2, 0, time.UTC),
		SheafVersion:   "v0.1.0",
		PRRefDisplayed: "example/lib@fedcba9",
	}
	art := BuildDeltaArtifact(in, base, head, c)
	if art.RendererVersion != RendererVersion {
		t.Errorf("renderer_version = %d, want %d", art.RendererVersion, RendererVersion)
	}
	if art.SchemaVersion != DeltaSchemaVersion {
		t.Errorf("schema_version = %q, want %q", art.SchemaVersion, DeltaSchemaVersion)
	}
	b1, err := MarshalDelta(art)
	if err != nil {
		t.Fatalf("MarshalDelta: %v", err)
	}
	b2, err := MarshalDelta(art)
	if err != nil {
		t.Fatalf("MarshalDelta (2nd): %v", err)
	}
	if string(b1) != string(b2) {
		t.Errorf("MarshalDelta is non-deterministic")
	}
	var round DeltaArtifact
	if err := json.Unmarshal(b1, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if round.PRRefDisplayed != "example/lib@fedcba9" {
		t.Errorf("round-trip lost PRRefDisplayed: %q", round.PRRefDisplayed)
	}
	if len(round.CoverageDelta.AffectedElements) != len(c.AffectedElements) {
		t.Errorf("affected count mismatch: %d vs %d",
			len(round.CoverageDelta.AffectedElements), len(c.AffectedElements))
	}
}

func TestRenderHTML_Deterministic(t *testing.T) {
	base, head := mkPair()
	rules := &categorizationpb.Rules{Version: 1}
	c, _ := Render(context.Background(), "example/lib@aaa1111", base, head, rules)
	art := BuildDeltaArtifact(DeltaInputs{
		System:         "lib",
		BaseShort:      "0123456",
		HeadShort:      "fedcba9",
		PRRefDisplayed: "example/lib@fedcba9",
		ScannedAt:      time.Date(2026, 5, 26, 18, 14, 2, 0, time.UTC),
	}, base, head, c)
	h1, err := RenderHTML(art)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	h2, _ := RenderHTML(art)
	if string(h1) != string(h2) {
		t.Errorf("RenderHTML is non-deterministic")
	}
	s := string(h1)
	if !strings.Contains(s, "sheaf-bot") {
		t.Errorf("HTML missing sheaf-bot chip")
	}
	if !strings.Contains(s, "example/lib@fedcba9") {
		t.Errorf("HTML missing PR ref")
	}
	if !strings.Contains(s, "lib/X") {
		t.Errorf("HTML missing affected element from body")
	}
}

func TestRenderHTML_NoCorpus(t *testing.T) {
	// A purely empty artifact (no scan) should still render without
	// crashing — this is what the "rerender from preserved JSON"
	// path looks like.
	art := &DeltaArtifact{
		SchemaVersion:   DeltaSchemaVersion,
		RendererVersion: RendererVersion,
		PRRefDisplayed:  "example/lib@deadbee",
		ScannedAt:       "2026-05-26T18:14:02Z",
		Title:           "Sheaf review · example/lib@deadbee",
		Body:            "## Sheaf review · example/lib@deadbee — touches 0 contract element(s)\n\n_No coverage-relevant changes._\n",
	}
	_, err := RenderHTML(art)
	if err != nil {
		t.Fatalf("RenderHTML on empty artifact: %v", err)
	}
}

func TestLoadDeltaArtifact_RoundTrip(t *testing.T) {
	art := &DeltaArtifact{
		SchemaVersion:   DeltaSchemaVersion,
		RendererVersion: RendererVersion,
		System:          "lib",
		BaseShort:       "aaa",
		HeadShort:       "bbb",
		PRRefDisplayed:  "example/lib@bbb",
		ScannedAt:       "2026-05-26T18:14:02Z",
		Body:            "## test\n\nhi\n",
	}
	b, err := MarshalDelta(art)
	if err != nil {
		t.Fatalf("MarshalDelta: %v", err)
	}
	dir := t.TempDir()
	path := dir + "/delta.json"
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := LoadDeltaArtifact(path)
	if err != nil {
		t.Fatalf("LoadDeltaArtifact: %v", err)
	}
	if loaded.PRRefDisplayed != art.PRRefDisplayed {
		t.Errorf("PRRefDisplayed lost in round trip")
	}
}
