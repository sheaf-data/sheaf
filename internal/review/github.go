package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// GitHub posts review comments via the GitHub REST API.
//
// API used:
//   POST /repos/{owner}/{repo}/issues/{number}/comments
//   { "body": "<body>" }
//
// PR-ref shape: "github:<owner>/<repo>#<number>" or just "<number>"
// if cfg.Repo is set.

type GitHubConfig struct {
	Repo       string // "owner/repo"; required if PR ref doesn't carry it
	TokenEnv   string // env var holding the PAT or app installation token
	HTTPClient *http.Client
}

type GitHub struct {
	cfg    GitHubConfig
	client *http.Client
}

func NewGitHub(cfg GitHubConfig) *GitHub {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &GitHub{cfg: cfg, client: cfg.HTTPClient}
}

func (g *GitHub) Name() string { return "github" }

func (g *GitHub) Post(ctx context.Context, prRef, commentBody string) (string, error) {
	repo, prNum, err := parsePRRef(prRef, g.cfg.Repo)
	if err != nil {
		return "", err
	}
	token := os.Getenv(g.cfg.TokenEnv)
	if token == "" {
		return "", fmt.Errorf("github: env %q is unset", g.cfg.TokenEnv)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments", repo, prNum)
	body, _ := json.Marshal(map[string]string{"body": commentBody})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("github POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("github POST %s: status %d: %s", url, resp.StatusCode, truncate(string(buf), 200))
	}
	return fmt.Sprintf("https://github.com/%s/pull/%d", repo, prNum), nil
}

// parsePRRef accepts:
//
//	"github:owner/repo#123"
//	"owner/repo#123"
//	"123"            (requires fallbackRepo)
//	"#123"           (requires fallbackRepo)
func parsePRRef(prRef, fallbackRepo string) (repo string, num int, err error) {
	s := strings.TrimPrefix(prRef, "github:")
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0, fmt.Errorf("github: empty PR ref")
	}
	if !strings.Contains(s, "#") {
		// Bare number — need fallback repo.
		s = strings.TrimSpace(s)
		if s == "" {
			return "", 0, fmt.Errorf("github: empty PR ref")
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return "", 0, fmt.Errorf("github: PR ref %q is not a number and lacks owner/repo", prRef)
		}
		if fallbackRepo == "" {
			return "", 0, fmt.Errorf("github: PR ref %q has no owner/repo and config.repo is empty", prRef)
		}
		return fallbackRepo, n, nil
	}
	parts := strings.SplitN(s, "#", 2)
	r := strings.TrimSpace(parts[0])
	if r == "" {
		r = fallbackRepo
	}
	if r == "" {
		return "", 0, fmt.Errorf("github: PR ref %q has no owner/repo and config.repo is empty", prRef)
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return "", 0, fmt.Errorf("github: PR number %q: %w", parts[1], err)
	}
	return r, n, nil
}
