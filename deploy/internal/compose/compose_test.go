package compose

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func homeserver(t *testing.T) Project {
	t.Helper()
	b, err := os.ReadFile("testdata/homeserver.json")
	require.NoError(t, err)
	p, err := Parse(b, "/code/repos/Homeserver")
	require.NoError(t, err)
	return p
}

func TestParseHomeserver(t *testing.T) {
	p := homeserver(t)
	assert.Equal(t, "homeserver", p.Name)
	nginx := p.Services["nginx"]
	var sources []string
	for _, m := range nginx.Mounts {
		sources = append(sources, m.Source)
	}
	assert.Contains(t, sources, "nginx/sites")
	assert.Contains(t, sources, "www/html")
	assert.True(t, nginx.Healthcheck)
	assert.Equal(t, "nginx -s reload", nginx.Labels["homeserver.reload"])
	assert.True(t, nginx.LongRunning())

	assert.Equal(t, "auth", p.Services["auth"].BuildContext)
	assert.True(t, p.Services["auth"].Healthcheck)
	assert.False(t, p.Services["certs"].LongRunning())
	assert.Equal(t, "720h", p.Services["certs"].Labels["homeserver.every"])
	assert.Equal(t, "postgres", p.Services["db"].Labels["homeserver.backup"])
	assert.Contains(t, p.Services["nginx"].DependsOn, "certs")
	assert.NotEmpty(t, p.Services["auth"].Raw)
	assert.NotContains(t, string(p.Services["nginx"].Raw), "/code/repos/Homeserver", "raw sources relative too")
}

func TestOrder(t *testing.T) {
	order := homeserver(t).Order()
	pos := map[string]int{}
	for i, s := range order {
		pos[s] = i
	}
	assert.Less(t, pos["certs"], pos["nginx"])
	assert.Less(t, pos["db"], pos["db-setup"])
	assert.Less(t, pos["db-setup"], pos["auth"])
	assert.Less(t, pos["auth"], pos["nginx"])
	assert.Len(t, order, len(homeserver(t).Services))
}

func TestLint(t *testing.T) {
	assert.Empty(t, Lint(homeserver(t)))
	p := Project{Services: map[string]Service{
		"web":   {Name: "web", Restart: "unless-stopped"},
		"once":  {Name: "once", Restart: "no"},
		"ok":    {Name: "ok", Restart: "always", Healthcheck: true},
		"skip":  {Name: "skip", Restart: "always", Labels: map[string]string{"homeserver.lint": "skip"}},
		"plain": {Name: "plain"},
	}}
	assert.Equal(t, []string{
		"service plain: long-running but has no healthcheck",
		"service web: long-running but has no healthcheck",
	}, Lint(p))
}

func TestParseDisabledHealthcheck(t *testing.T) {
	p, err := Parse([]byte(`{"name":"x","services":{"a":{"image":"busybox","healthcheck":{"disable":true}}}}`), "/x")
	require.NoError(t, err)
	assert.False(t, p.Services["a"].Healthcheck)
}

func TestParseBuildPaths(t *testing.T) {
	p, err := Parse([]byte(`{"name":"x","services":{"web":{"build":{
		"context":"/x/web","dockerfile":"../docker/web.Dockerfile",
		"additional_contexts":{"shared":"/x/shared","img":"docker-image://alpine","svc":"service:base","remote":"https://example.com/x.git"}},
		"healthcheck":{"test":["CMD","true"]}}}}`), "/x")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"web", "docker/web.Dockerfile", "shared"}, p.Services["web"].BuildPaths)
}
