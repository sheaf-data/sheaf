package corpus

import (
	"reflect"
	"sync"
	"testing"

	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

func TestCorpus_AddAndQuery(t *testing.T) {
	c := New()
	c.AddElement(&contractpb.ContractElement{Id: "fuchsia.io/Directory.Open"})
	c.AddElement(&contractpb.ContractElement{Id: "fuchsia.io/Directory.Close"})
	c.AddTest(&testcasepb.TestCase{Id: "DirTest.OpenWorks"})
	c.AddDocClaim(&docclaimpb.DocClaim{SourcePath: "docs/io.md"})
	c.SetProfile(&coveragepb.CoverageProfile{ElementId: "fuchsia.io/Directory.Open"})

	if s := c.Stats(); s.Elements != 2 || s.Tests != 1 || s.DocClaims != 1 || s.Profiles != 1 {
		t.Errorf("Stats = %+v", s)
	}

	want := []string{"fuchsia.io/Directory.Close", "fuchsia.io/Directory.Open"}
	if got := c.ElementIDs(); !reflect.DeepEqual(got, want) {
		t.Errorf("ElementIDs = %v, want %v", got, want)
	}

	if e := c.Element("fuchsia.io/Directory.Open"); e == nil {
		t.Errorf("missing element after add")
	}
	if e := c.Element("nonexistent"); e != nil {
		t.Errorf("got non-nil for missing element: %v", e)
	}
	if p := c.Profile("fuchsia.io/Directory.Open"); p == nil {
		t.Errorf("missing profile after set")
	}
}

func TestCorpus_NilSafe(t *testing.T) {
	c := New()
	c.AddElement(nil)
	c.AddTest(nil)
	c.AddDocClaim(nil)
	c.SetProfile(nil)
	c.AddElement(&contractpb.ContractElement{}) // empty ID
	if s := c.Stats(); s.Elements != 0 || s.Tests != 0 || s.DocClaims != 0 || s.Profiles != 0 {
		t.Errorf("nil/empty adds shouldn't count: %+v", s)
	}
}

func TestCorpus_DuplicateIDReplaces(t *testing.T) {
	c := New()
	c.AddElement(&contractpb.ContractElement{Id: "x", Library: "old"})
	c.AddElement(&contractpb.ContractElement{Id: "x", Library: "new"})
	if got := c.Element("x").GetLibrary(); got != "new" {
		t.Errorf("library = %q, want new", got)
	}
}

func TestCorpus_ConcurrentAdd(t *testing.T) {
	c := New()
	var wg sync.WaitGroup
	const n = 100
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.AddElement(&contractpb.ContractElement{Id: string(rune('a'+(i%26))) + string(rune('0'+(i/26)))})
			c.AddTest(&testcasepb.TestCase{Id: "t" + string(rune('a'+(i%26)))})
		}(i)
	}
	wg.Wait()
	s := c.Stats()
	if s.Elements == 0 || s.Tests == 0 {
		t.Errorf("expected nonzero concurrent inserts; got %+v", s)
	}
}

func TestCorpus_BulkAdd(t *testing.T) {
	c := New()
	c.AddElements([]*contractpb.ContractElement{
		{Id: "a"}, {Id: "b"}, {Id: "c"},
	})
	c.AddTests([]*testcasepb.TestCase{
		{Id: "t1"}, {Id: "t2"},
	})
	c.AddDocClaims([]*docclaimpb.DocClaim{
		{SourcePath: "x.md"}, {SourcePath: "y.md"},
	})
	s := c.Stats()
	if s.Elements != 3 || s.Tests != 2 || s.DocClaims != 2 {
		t.Errorf("bulk add wrong: %+v", s)
	}
}
