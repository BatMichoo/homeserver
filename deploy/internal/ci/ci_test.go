package ci

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func run(name, status, conclusion, started string) string {
	return fmt.Sprintf(`{"name":%q,"status":%q,"conclusion":%q,"started_at":%q,"app":{"slug":"github-actions"}}`,
		name, status, conclusion, started)
}

func serve(t *testing.T, code int, body string) Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/me/repo/commits/abc/check-runs", r.URL.Path)
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return Client{HTTP: srv.Client(), BaseURL: srv.URL}
}

func runs(rs ...string) string { return `{"check_runs":[` + strings.Join(rs, ",") + `]}` }

func TestCIStatus(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		body string
		want Status
	}{
		"none":          {runs(), Pending},
		"running":       {runs(run("test", "in_progress", "", "2026-11-15T10:00:00Z")), Pending},
		"all green":     {runs(run("test", "completed", "success", "2026-11-15T10:00:00Z"), run("lint", "completed", "skipped", "2026-11-15T10:00:00Z")), Success},
		"one failed":    {runs(run("test", "completed", "failure", "2026-11-15T10:00:00Z"), run("lint", "completed", "success", "2026-11-15T10:00:00Z")), Failure},
		"rerun fixed":   {runs(run("test", "completed", "failure", "2026-11-15T10:00:00Z"), run("test", "completed", "success", "2026-11-15T11:00:00Z")), Success},
		"rerun pending": {runs(run("test", "completed", "success", "2026-11-15T10:00:00Z"), run("test", "queued", "", "2026-11-15T11:00:00Z")), Pending},
		"other apps ignored": {runs(run("test", "completed", "success", "2026-11-15T10:00:00Z"),
			`{"name":"x","status":"completed","conclusion":"failure","started_at":"2026-11-15T10:00:00Z","app":{"slug":"codecov"}}`), Success},
	} {
		assert.Equal(t, c.want, serve(t, 200, c.body).Status(ctx, "me/repo", "abc"), name)
	}
}

func TestCIStatusErrorsArePending(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, Pending, serve(t, 500, "boom").Status(ctx, "me/repo", "abc"))
	assert.Equal(t, Pending, serve(t, 403, `{"message":"API rate limit exceeded"}`).Status(ctx, "me/repo", "abc"))
	assert.Equal(t, Pending, serve(t, 200, "not json").Status(ctx, "me/repo", "abc"))
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	assert.Equal(t, Pending, Client{HTTP: srv.Client(), BaseURL: srv.URL}.Status(ctx, "me/repo", "abc"))
}

func TestCISendsToken(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		fmt.Fprint(w, runs())
	}))
	defer srv.Close()
	Client{HTTP: srv.Client(), BaseURL: srv.URL, Token: "tok"}.Status(context.Background(), "me/repo", "abc")
	assert.Equal(t, "Bearer tok", auth)
}

func TestCILogsWhy(t *testing.T) {
	var buf strings.Builder
	c := serve(t, 403, `{"message":"API rate limit exceeded"}`)
	c.Log = log.New(&buf, "", 0)
	c.Status(context.Background(), "me/repo", "abc")
	assert.Contains(t, buf.String(), "403")
}
