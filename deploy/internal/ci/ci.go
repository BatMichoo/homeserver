// Package ci reads a commit's GitHub Actions results.
package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Status of a commit's CI.
type Status string

const (
	Success Status = "success"
	Pending Status = "pending" // also: couldn't tell (network, rate limit)
	Failure Status = "failure"
)

// Client asks GitHub's check-runs API.
type Client struct {
	HTTP    *http.Client
	BaseURL string // default https://api.github.com
	Token   string // optional, for rate limits
}

type checkRun struct {
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	StartedAt  time.Time `json:"started_at"`
	App        struct {
		Slug string `json:"slug"`
	} `json:"app"`
}

// Status of sha in repo (owner/name). Only GitHub Actions runs count, and
// only the latest attempt of each job (a successful re-run supersedes a
// failure). Anything that keeps us from knowing is Pending, so a commit is
// never deployed or skipped on a guess.
func (c Client) Status(ctx context.Context, repo, sha string) Status {
	base := c.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/commits/%s/check-runs?per_page=100", base, repo, sha), nil)
	if err != nil {
		return Pending
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := hc.Do(req)
	if err != nil {
		return Pending
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Pending
	}
	var body struct {
		CheckRuns []checkRun `json:"check_runs"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return Pending
	}

	latest := map[string]checkRun{}
	for _, r := range body.CheckRuns {
		if r.App.Slug != "github-actions" {
			continue
		}
		if prev, ok := latest[r.Name]; !ok || r.StartedAt.After(prev.StartedAt) {
			latest[r.Name] = r
		}
	}
	if len(latest) == 0 {
		return Pending
	}
	result := Success
	for _, r := range latest {
		if r.Status != "completed" {
			return Pending
		}
		switch r.Conclusion {
		case "success", "skipped", "neutral":
		default:
			result = Failure
		}
	}
	return result
}
