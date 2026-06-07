package scanner

import (
	"os"
	"testing"
)

// TestRenderFixtureForVisual writes a rendered HTML report to a known
// path using a synthetic Snapshot that exercises every UpSet row,
// fix-group and anomaly group. Skipped unless SHEAF_DUMP=1 so it doesn't
// run in CI. The output is meant for eyeballing against the demo file.
func TestRenderFixtureForVisual(t *testing.T) {
	if os.Getenv("SHEAF_DUMP") != "1" {
		t.Skip("set SHEAF_DUMP=1 to dump fixture HTML")
	}

	elements := []map[string]any{}
	profiles := []map[string]any{}

	add := func(id string, concept, test, example int, subst string) {
		elements = append(elements, map[string]any{
			"id":       "lib.demo/" + id,
			"kind":     "METHOD",
			"library":  "lib.demo",
			"location": map[string]any{"path": "src/" + id + ".go", "line": float64(1)},
		})
		prof := map[string]any{"elementId": "lib.demo/" + id}
		docs := map[string]any{}
		if concept > 0 {
			arr := []any{}
			for i := 0; i < concept; i++ {
				arr = append(arr, map[string]any{
					"path": "docs/" + id + ".md", "line": float64(i + 1),
					"substance": subst, "words": float64(120),
				})
			}
			docs["concept"] = arr
		}
		prof["docs"] = docs
		if test > 0 {
			arr := []any{}
			for i := 0; i < test; i++ {
				arr = append(arr, map[string]any{
					"path": "test/" + id + "_test.go", "line": float64(i + 10),
					"testName": id + "Test",
				})
			}
			prof["tests"] = map[string]any{"unit": arr}
		}
		if example > 0 {
			arr := []any{}
			for i := 0; i < example; i++ {
				arr = append(arr, map[string]any{
					"path": "examples/" + id + ".go", "line": float64(1),
				})
			}
			prof["examples"] = map[string]any{"inTree": arr}
		}
		profiles = append(profiles, prof)
	}

	// Counts mirror the golden demo so the visual lands at the same shape.
	for i := 0; i < 27; i++ {
		add("Asserted"+itoa(i), 1, 0, 0, "PARTIAL")
	}
	for i := 0; i < 19; i++ {
		add("Exercised"+itoa(i), 0, 1, 0, "ABSENT")
	}
	for i := 0; i < 14; i++ {
		add("Established"+itoa(i), 1, 2, 0, "SUBSTANTIVE")
	}
	for i := 0; i < 11; i++ {
		add("Unclaimed"+itoa(i), 0, 0, 0, "ABSENT")
	}
	for i := 0; i < 8; i++ {
		add("Completed"+itoa(i), 1, 1, 1, "SUBSTANTIVE")
	}
	for i := 0; i < 5; i++ {
		add("Shown"+itoa(i), 1, 0, 1, "SUBSTANTIVE")
	}
	for i := 0; i < 3; i++ {
		add("Practiced"+itoa(i), 0, 1, 1, "ABSENT")
	}
	for i := 0; i < 2; i++ {
		add("Sketched"+itoa(i), 0, 0, 1, "ABSENT")
	}

	// addDep seeds an element with a deprecation marker so the
	// fixture exercises the "deprecated" and "removed" buckets that
	// roll up into the Worklist's combined caption (rather than
	// appearing as rows). target API level for this fixture is "HEAD";
	// per versionscheme.FIDL, removed != "NEXT" is treated as gone.
	addDep := func(id, deprecated, removed string) {
		el := map[string]any{
			"id":       "lib.demo/" + id,
			"kind":     "METHOD",
			"library":  "lib.demo",
			"location": map[string]any{"path": "src/" + id + ".go", "line": float64(1)},
		}
		c := map[string]any{"ecosystem": "fidl"}
		if deprecated != "" {
			c["deprecated"] = deprecated
		}
		if removed != "" {
			c["removed"] = removed
		}
		el["versionConstraints"] = []any{c}
		elements = append(elements, el)
		// No surface profile — these don't appear in the Worklist
		// rows, only in the caption count.
		profiles = append(profiles, map[string]any{"elementId": "lib.demo/" + id})
	}
	for i := 0; i < 4; i++ {
		addDep("Deprecated"+itoa(i), "v1", "")
	}
	for i := 0; i < 3; i++ {
		addDep("Gone"+itoa(i), "", "HEAD")
	}

	findings := []map[string]any{
		{
			"id": "f1", "kind": "STALE_DOC",
			"subject": "lib.demo/Established0", "severity": "WARNING",
			"analyzer": "stale-doc", "message": "doc references removed parameter 'force'",
			"evidence": []any{
				map[string]any{
					"description": "doc reference",
					"location":    map[string]any{"path": "docs/Established0.md", "line": float64(3)},
				},
			},
		},
		{
			"id": "f2", "kind": "TESTED_UNDOCUMENTED",
			"subject": "lib.demo/Exercised0", "severity": "INFO",
			"analyzer": "naming", "message": "test exercises a call no doc names",
			"evidence": []any{
				map[string]any{
					"description": "test reference",
					"location":    map[string]any{"path": "test/Exercised0_test.go", "line": float64(10)},
				},
			},
		},
		{
			"id": "f3", "kind": "STALE_DOC",
			"subject": "lib.demo/Established1", "severity": "ERROR",
			"analyzer": "stale-doc", "message": "doc references method removed in HEAD",
			"evidence": []any{
				map[string]any{
					"description": "doc reference",
					"location":    map[string]any{"path": "docs/Established1.md", "line": float64(7)},
				},
			},
		},
	}

	snap := &Snapshot{Library: "lib.demo", Elements: elements, Profiles: profiles, Findings: findings}
	r := BuildReportWithOptions(snap, "proto", "2026-05-25 12:00 UTC", "HEAD", "", "", "minimal", "abc1234")

	out, err := os.Create("/tmp/sheaf-render-fixture.html")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := RenderHTML(out, r); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote /tmp/sheaf-render-fixture.html")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
