package buildgraph

import (
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
)

// stubHints lets a test declare arbitrary IsPublic / FacadeOf answers
// for a small fixed set of paths.
type stubHints struct {
	pub    map[string]bool // relPath -> isPublic; presence in map => known=true
	facade map[string][2]string
}

func (s stubHints) IsPublic(relPath string) (bool, bool) {
	if v, ok := s.pub[relPath]; ok {
		return v, true
	}
	return false, false
}

func (s stubHints) FacadeOf(relPath string) (string, string, bool) {
	if v, ok := s.facade[relPath]; ok {
		return v[0], v[1], true
	}
	return "", "", false
}

func (s stubHints) FacadeModule(string) (string, bool)         { return "", false }
func (s stubHints) FacadeSymbol(string, string) (string, bool) { return "", false }

func TestComposite_EmptyReturnsNopHints(t *testing.T) {
	c := Composite()
	if _, ok := c.(adapters.NopHints); !ok {
		t.Fatalf("empty composite: want NopHints, got %T", c)
	}
	if ok, known := c.IsPublic("foo.h"); ok || known {
		t.Errorf("NopHints IsPublic: want (false, false), got (%v, %v)", ok, known)
	}
	if f, b, ok := c.FacadeOf("foo.h"); ok || f != "" || b != "" {
		t.Errorf("NopHints FacadeOf: want zero, got (%q, %q, %v)", f, b, ok)
	}
}

func TestComposite_FirstWins(t *testing.T) {
	a := stubHints{
		pub:    map[string]bool{"a.h": true},
		facade: map[string][2]string{"a.h": {"pw_chrono", "pw_chrono_stl"}},
	}
	b := stubHints{
		pub:    map[string]bool{"a.h": false, "b.h": true},
		facade: map[string][2]string{"a.h": {"other", "other_x"}, "b.h": {"pw_log", "pw_log_basic"}},
	}
	c := Composite(a, b)

	// a wins for a.h.
	if ok, known := c.IsPublic("a.h"); !known || !ok {
		t.Errorf("a.h IsPublic: want (true, true), got (%v, %v)", ok, known)
	}
	if f, bk, ok := c.FacadeOf("a.h"); !ok || f != "pw_chrono" || bk != "pw_chrono_stl" {
		t.Errorf("a.h FacadeOf: want (pw_chrono, pw_chrono_stl, true), got (%q, %q, %v)", f, bk, ok)
	}
	// b fills in for b.h.
	if ok, known := c.IsPublic("b.h"); !known || !ok {
		t.Errorf("b.h IsPublic: want (true, true), got (%v, %v)", ok, known)
	}
	if f, bk, ok := c.FacadeOf("b.h"); !ok || f != "pw_log" || bk != "pw_log_basic" {
		t.Errorf("b.h FacadeOf: want (pw_log, pw_log_basic, true), got (%q, %q, %v)", f, bk, ok)
	}
	// Neither knows about c.h.
	if ok, known := c.IsPublic("c.h"); known || ok {
		t.Errorf("c.h IsPublic: want (false, false), got (%v, %v)", ok, known)
	}
	if f, bk, ok := c.FacadeOf("c.h"); ok || f != "" || bk != "" {
		t.Errorf("c.h FacadeOf: want zero, got (%q, %q, %v)", f, bk, ok)
	}
}

func TestComposite_NopHintsPassthrough(t *testing.T) {
	// A NopHints in the chain should never short-circuit a subsequent
	// recognizer's answer.
	real := stubHints{
		pub:    map[string]bool{"x.h": true},
		facade: map[string][2]string{"x.h": {"pw_chrono", "pw_chrono_stl"}},
	}
	c := Composite(adapters.NopHints{}, real)

	if ok, known := c.IsPublic("x.h"); !known || !ok {
		t.Errorf("x.h IsPublic: want (true, true), got (%v, %v)", ok, known)
	}
	if f, bk, ok := c.FacadeOf("x.h"); !ok || f != "pw_chrono" || bk != "pw_chrono_stl" {
		t.Errorf("x.h FacadeOf: want (pw_chrono, pw_chrono_stl, true), got (%q, %q, %v)", f, bk, ok)
	}
}

func TestComposite_SingleHintReturnedUnwrapped(t *testing.T) {
	s := stubHints{pub: map[string]bool{"k.h": true}}
	c := Composite(s)
	if _, ok := c.(stubHints); !ok {
		t.Errorf("single hint: want unwrapped stubHints, got %T", c)
	}
	if ok, known := c.IsPublic("k.h"); !ok || !known {
		t.Errorf("unwrapped behavior: want (true, true), got (%v, %v)", ok, known)
	}
}

func TestComposite_NilsDropped(t *testing.T) {
	c := Composite(nil, nil, nil)
	if _, ok := c.(adapters.NopHints); !ok {
		t.Fatalf("all-nil composite: want NopHints, got %T", c)
	}
}
