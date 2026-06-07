package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Gerrit posts review comments to a Gerrit server via the REST API.
//
// API used:
//   POST /a/changes/{change-id}/revisions/current/review
//   { "message": "<body>", "tag": "sheaf-bot" }
//
// Auth: HTTP Basic auth with username + the password from the env
// var configured in AuthTokenEnv. The username comes from the env
// var SHEAF_GERRIT_USER, or "sheaf-bot" by default.
//
// The PR-ref shape expected: "gerrit:<change-id>" or just "<change-id>".

type GerritConfig struct {
	Host         string // e.g. "fuchsia-review.googlesource.com"
	Project      string // e.g. "fuchsia"
	AuthTokenEnv string // env var holding the HTTP password
	UserEnv      string // optional env var holding the username; default SHEAF_GERRIT_USER
	HTTPClient   *http.Client
}

type Gerrit struct {
	cfg    GerritConfig
	client *http.Client
}

func NewGerrit(cfg GerritConfig) *Gerrit {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Gerrit{cfg: cfg, client: cfg.HTTPClient}
}

func (g *Gerrit) Name() string { return "gerrit" }

func (g *Gerrit) Post(ctx context.Context, prRef, commentBody string) (string, error) {
	changeID := strings.TrimPrefix(prRef, "gerrit:")
	if changeID == "" {
		return "", fmt.Errorf("gerrit: empty change-id from %q", prRef)
	}
	password := os.Getenv(g.cfg.AuthTokenEnv)
	if password == "" {
		return "", fmt.Errorf("gerrit: env %q is unset", g.cfg.AuthTokenEnv)
	}
	userEnv := g.cfg.UserEnv
	if userEnv == "" {
		userEnv = "SHEAF_GERRIT_USER"
	}
	user := os.Getenv(userEnv)
	if user == "" {
		user = "sheaf-bot"
	}
	url := fmt.Sprintf("https://%s/a/changes/%s/revisions/current/review", g.cfg.Host, changeID)
	body, _ := json.Marshal(map[string]any{
		"message": commentBody,
		"tag":     "sheaf-bot",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(user, password)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("gerrit POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("gerrit POST %s: status %d: %s", url, resp.StatusCode, truncate(string(buf), 200))
	}
	return url, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
