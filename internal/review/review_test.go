package review

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoop(t *testing.T) {
	a := Noop{}
	if a.Name() != "noop" {
		t.Errorf("name = %q", a.Name())
	}
	got, err := a.Post(context.Background(), "PR#1", "anything")
	if err != nil {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(got, "PR#1") {
		t.Errorf("expected PR#1 in output, got %q", got)
	}
}

func TestFile_SingleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "comment.md")
	a := NewFile(path)
	got, err := a.Post(context.Background(), "PR#42", "## body of comment")
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if !strings.HasSuffix(got, path) {
		t.Errorf("returned path = %q, want suffix %s", got, path)
	}
	body, _ := os.ReadFile(path)
	if string(body) != "## body of comment" {
		t.Errorf("file contents = %q", string(body))
	}
}

func TestFile_DirOnePerPR(t *testing.T) {
	dir := t.TempDir()
	a := NewFileDir(dir)
	if _, err := a.Post(context.Background(), "PR#42", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Post(context.Background(), "PR#43", "b"); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	if len(files) != 2 {
		t.Errorf("got %d files, want 2: %v", len(files), files)
	}
}

func TestFile_NewFromEnv_Default(t *testing.T) {
	t.Setenv("SHEAF_REVIEW_FILE_OUT", "")
	a := NewFileFromEnv()
	if a.OutputDir == "" {
		t.Errorf("expected default OutputDir; got %+v", a)
	}
}

func TestFile_SlugifyForFile(t *testing.T) {
	cases := map[string]string{
		"PR#4521":           "PR-4521",
		"github:foo/bar#1":  "github-foo-bar-1",
		"":                  "review",
		"  ###  ":           "review",
		"gerrit:I123abc456": "gerrit-I123abc456",
	}
	for in, want := range cases {
		got := slugifyForFile(in)
		if got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGitHub_Post_HappyPath(t *testing.T) {
	t.Setenv("GH_TOKEN", "fake-pat")
	var receivedBody map[string]any
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &receivedBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()
	g := NewGitHub(GitHubConfig{Repo: "owner/repo", TokenEnv: "GH_TOKEN", HTTPClient: srv.Client()})
	g.client = httpRewriteTransport(srv.URL, g.client)
	got, err := g.Post(context.Background(), "github:owner/repo#42", "hello")
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if got != "https://github.com/owner/repo/pull/42" {
		t.Errorf("returned URL = %q", got)
	}
	if receivedAuth != "Bearer fake-pat" {
		t.Errorf("Authorization = %q", receivedAuth)
	}
	if receivedBody["body"] != "hello" {
		t.Errorf("body = %v", receivedBody)
	}
}

func TestGitHub_Post_MissingToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	g := NewGitHub(GitHubConfig{Repo: "owner/repo", TokenEnv: "GH_TOKEN"})
	_, err := g.Post(context.Background(), "github:owner/repo#1", "x")
	if err == nil || !strings.Contains(err.Error(), "GH_TOKEN") {
		t.Errorf("expected env-unset error; got %v", err)
	}
}

func TestParsePRRef(t *testing.T) {
	cases := []struct {
		in, fallback, wantRepo string
		wantNum                int
		wantErr                bool
	}{
		{"github:foo/bar#42", "", "foo/bar", 42, false},
		{"foo/bar#42", "", "foo/bar", 42, false},
		{"42", "foo/bar", "foo/bar", 42, false},
		{"#42", "foo/bar", "foo/bar", 42, false},
		{"42", "", "", 0, true},              // no fallback
		{"github:foo/bar", "", "", 0, true},  // no '#'
		{"github:foo/bar#", "", "", 0, true}, // empty number
		{"", "foo/bar", "", 0, true},
	}
	for _, c := range cases {
		r, n, err := parsePRRef(c.in, c.fallback)
		if (err != nil) != c.wantErr {
			t.Errorf("parsePRRef(%q, %q) err = %v, wantErr=%v", c.in, c.fallback, err, c.wantErr)
			continue
		}
		if r != c.wantRepo || n != c.wantNum {
			t.Errorf("parsePRRef(%q, %q) = (%q, %d), want (%q, %d)", c.in, c.fallback, r, n, c.wantRepo, c.wantNum)
		}
	}
}

func TestGerrit_Post_HappyPath(t *testing.T) {
	t.Setenv("GERRIT_PW", "abc123")
	var receivedAuth string
	var receivedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	g := NewGerrit(GerritConfig{
		Host:         "fuchsia-review.googlesource.com",
		Project:      "fuchsia",
		AuthTokenEnv: "GERRIT_PW",
	})
	g.client = httpRewriteTransport(srv.URL, g.client)
	got, err := g.Post(context.Background(), "gerrit:I12345", "hi")
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if !strings.Contains(got, "I12345") {
		t.Errorf("URL = %q", got)
	}
	if !strings.HasPrefix(receivedAuth, "Basic ") {
		t.Errorf("Authorization = %q, want Basic", receivedAuth)
	}
	if receivedBody["message"] != "hi" {
		t.Errorf("message = %v", receivedBody["message"])
	}
	if receivedBody["tag"] != "sheaf-bot" {
		t.Errorf("tag = %v", receivedBody["tag"])
	}
}

// httpRewriteTransport returns a client whose Transport rewrites
// every request's host to target. Used to point real-looking
// http.Client calls at an httptest.Server.
func httpRewriteTransport(target string, base *http.Client) *http.Client {
	return &http.Client{
		Transport: &rewriteRT{base: base.Transport, target: target},
		Timeout:   base.Timeout,
	}
}

type rewriteRT struct {
	base   http.RoundTripper
	target string // e.g. "http://127.0.0.1:12345"
}

func (r *rewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	newURL, err := req.URL.Parse(r.target + req.URL.RequestURI())
	if err != nil {
		return nil, err
	}
	req2 := req.Clone(req.Context())
	req2.URL = newURL
	req2.Host = ""
	t := r.base
	if t == nil {
		t = http.DefaultTransport
	}
	return t.RoundTrip(req2)
}
