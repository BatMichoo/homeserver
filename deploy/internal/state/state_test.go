package state

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(dir)
	require.NoError(t, err)
	assert.Empty(t, s.Held)

	s.Deployed.Commit, s.Deployed.Subject, s.Deployed.At = "abc", "msg", t0
	s.LastResult = "partial"
	s.Skipped = []string{"bad"}
	s.Held = map[string]Held{"web": {Service: "web", Commit: "abc", Reason: "unhealthy", Since: t0, Image: "sha256:1",
		Definition: json.RawMessage(`{"image":"web:1"}`)}}
	s.LastRun = map[string]time.Time{"certs": t0}
	s.CertExpiry = t0.Add(400 * 24 * time.Hour)
	s.Deploying = &Deploying{From: "a", To: "b"}
	s.Add(Event{Time: t0, Kind: "partial", Commit: "abc", Message: "web held"})
	require.NoError(t, s.Save(dir))

	got, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, s, got)
	assert.True(t, got.IsSkipped("bad"))
	assert.False(t, got.IsSkipped("good"))
}

func TestEventsCapped(t *testing.T) {
	s := &State{}
	for i := 0; i < 60; i++ {
		s.Add(Event{Time: t0.Add(time.Duration(i) * time.Minute), Kind: "deployed", Message: "x"})
	}
	assert.Len(t, s.Events, 50)
	assert.Equal(t, t0.Add(59*time.Minute), s.Events[49].Time, "newest last")
}

func TestSaveAtomic(t *testing.T) {
	dir := t.TempDir()
	s := &State{LastResult: "deployed"}
	require.NoError(t, s.Save(dir))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	s.LastResult = "partial"
	assert.Error(t, s.Save(dir))
	got, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "deployed", got.LastResult, "old file intact")
}

func TestStatusShape(t *testing.T) {
	dir := t.TempDir()
	s := &State{LastResult: "partial", CertExpiry: t0}
	s.Deployed.Commit, s.Deployed.Subject, s.Deployed.At = "abcdef1234", "msg", t0
	s.Held = map[string]Held{"web": {Service: "web", Commit: "abc", Reason: "unhealthy", Since: t0, Image: "sha256:secret",
		Definition: json.RawMessage(`{"environment":{"PASSWORD":"x"}}`)}}
	for i := 0; i < 30; i++ {
		s.Add(Event{Time: t0, Kind: "deployed", Message: "x"})
	}
	require.NoError(t, WriteStatus(dir, s))
	b, err := os.ReadFile(filepath.Join(dir, "public", "status.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(b), "PASSWORD")
	assert.NotContains(t, string(b), "sha256:secret")
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	assert.EqualValues(t, 1, got["version"])
	assert.Equal(t, "partial", got["last_result"])
	assert.Equal(t, "abcdef1234", got["deployed"].(map[string]any)["commit"])
	held := got["held"].([]any)
	require.Len(t, held, 1)
	assert.Equal(t, "web", held[0].(map[string]any)["service"])
	assert.Equal(t, "unhealthy", held[0].(map[string]any)["reason"])
	assert.Len(t, got["events"], 20)
	assert.Contains(t, got, "cert_expiry")
}

func TestOverrideYAMLGolden(t *testing.T) {
	dir := t.TempDir()
	held := map[string]Held{"web": {Service: "web", Image: "sha256:1", Definition: json.RawMessage(`{"image":"web:1","environment":{"X":"old"}}`)}}
	newDefs := map[string]json.RawMessage{"web": json.RawMessage(`{"build":{"context":"web"},"environment":{"X":"new","Y":"y"},"ports":[]}`)}
	require.NoError(t, WriteOverride(dir, held, newDefs))
	b, err := os.ReadFile(filepath.Join(dir, "override.yaml"))
	require.NoError(t, err)
	assert.Equal(t, `# Written by the deployer: services held on their previous version.
services:
  web:
    build: !reset null
    environment: !override {"X":"old"}
    image: !override "sha256:1"
    ports: !reset null
    pull_policy: !override "never"
`, string(b))
}

func TestOverrideNotDeployedProfile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteOverride(dir, map[string]Held{"new": {Service: "new", Reason: "unhealthy"}}, nil))
	b, _ := os.ReadFile(filepath.Join(dir, "override.yaml"))
	assert.Contains(t, string(b), "  new:\n    profiles: !override [\"held\"]\n")
}

func TestOverrideRemovedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteOverride(dir, map[string]Held{"w": {Service: "w", Image: "sha256:1", Definition: json.RawMessage(`{}`)}}, nil))
	assert.FileExists(t, filepath.Join(dir, "override.yaml"))
	require.NoError(t, WriteOverride(dir, nil, nil))
	assert.NoFileExists(t, filepath.Join(dir, "override.yaml"))
	require.NoError(t, WriteOverride(dir, nil, nil), "nothing to remove is fine")
}

// The override really makes compose resolve the old definition.
func TestOverrideResolves(t *testing.T) {
	if exec.Command("docker", "compose", "version").Run() != nil {
		t.Skip("docker compose not available")
	}
	dir := t.TempDir()
	base := `services:
  web:
    build:
      context: ./web
    environment:
      X: new
      Y: only-new
    ports:
      - "8080:80"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(base), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "web"), 0o755))
	held := map[string]Held{"web": {Service: "web", Image: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Definition: json.RawMessage(`{"image":"web:old","environment":{"X":"old"},"restart":"always"}`)}}
	newDefs := map[string]json.RawMessage{"web": json.RawMessage(`{"build":{"context":"web"},"environment":{"X":"new","Y":"only-new"},"ports":[{"target":80,"published":"8080"}]}`)}
	require.NoError(t, WriteOverride(dir, held, newDefs))

	cmd := exec.Command("docker", "compose", "-f", "compose.yaml", "-f", "override.yaml", "config", "--format", "json")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	var cfg struct {
		Services map[string]map[string]any `json:"services"`
	}
	require.NoError(t, json.Unmarshal(out, &cfg))
	web := cfg.Services["web"]
	assert.Equal(t, map[string]any{"X": "old"}, web["environment"])
	assert.Equal(t, held["web"].Image, web["image"])
	assert.Equal(t, "never", web["pull_policy"])
	assert.Equal(t, "always", web["restart"])
	assert.NotContains(t, web, "build")
	assert.NotContains(t, web, "ports")
}
