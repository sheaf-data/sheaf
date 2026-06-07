package cppusage

import (
	"reflect"
	"sort"
	"testing"
)

func sortedExtract(e *Extractor, body string) []string {
	got := e.Extract(body)
	sort.Strings(got)
	return got
}

// A pw_status-style usage snippet: macros, free functions, a qualified return
// type, plus distractors (a bare lowercase method, a capitalized bare method,
// and a foreign namespace) that must NOT be extracted.
const snippet = `
#include "pw_status/try.h"

pw::Status DoThing() {
  PW_TRY(SubOp1());
  PW_TRY_WITH_SIZE(SubOp2());
  if (!status.ok()) {
    return status;
  }
  status.Update(pw::Status::Internal());
  std::vector<int> v;
  return pw::OkStatus();
}`

func TestExtract_MacrosAndQualifiedNames(t *testing.T) {
	got := sortedExtract(New("pw"), snippet)
	want := []string{
		"PW_TRY", "PW_TRY_WITH_SIZE",
		"pw::OkStatus", "pw::Status", "pw::Status::Internal",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Extract(prefix=pw) = %v, want %v", got, want)
	}
}

func TestExtract_DoesNotLeakBareMethodsOrForeignNamespaces(t *testing.T) {
	got := New("pw").Extract(snippet)
	for _, bad := range []string{"ok", "Update", "std::vector", "DoThing", "SubOp1"} {
		for _, g := range got {
			if g == bad {
				t.Errorf("must not extract %q (got %v)", bad, got)
			}
		}
	}
}

func TestExtract_EmptyPrefix_MacrosOnly(t *testing.T) {
	got := sortedExtract(New(""), snippet)
	want := []string{"PW_TRY", "PW_TRY_WITH_SIZE"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Extract(prefix=\"\") = %v, want %v", got, want)
	}
}

func TestExtract_Dedups(t *testing.T) {
	got := New("pw").Extract("PW_TRY(a); PW_TRY(b); pw::OkStatus(); pw::OkStatus();")
	if len(got) != 2 {
		t.Errorf("expected 2 deduped refs, got %v", got)
	}
}
