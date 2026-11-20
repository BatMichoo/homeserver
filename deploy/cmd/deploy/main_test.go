package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLintCommand(t *testing.T) {
	var out bytes.Buffer
	bad := `{"name":"x","services":{"web":{"image":"busybox"}}}`
	assert.Equal(t, 1, lint([]string{"--project-dir", "/x"}, strings.NewReader(bad), &out))
	assert.Contains(t, out.String(), "service web")
	out.Reset()
	good := `{"name":"x","services":{"web":{"image":"busybox","healthcheck":{"test":["CMD","true"]}}}}`
	assert.Equal(t, 0, lint(nil, strings.NewReader(good), &out))
	assert.Equal(t, 2, lint(nil, strings.NewReader("nope"), &out))
}

// A service that reloads in place may mount folders, not single files.
func TestLintCommandFileMounts(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.conf"), nil, 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sites"), 0o755))
	cfg := func(src string) string {
		return `{"name":"x","services":{"web":{"image":"nginx","healthcheck":{"test":["CMD","true"]},
			"labels":{"homeserver.reload":"nginx -s reload"},
			"volumes":[{"type":"bind","source":"` + filepath.Join(dir, src) + `","target":"/etc/x"}]}}}`
	}
	var out bytes.Buffer
	assert.Equal(t, 1, lint([]string{"--project-dir", dir}, strings.NewReader(cfg("main.conf")), &out))
	assert.Contains(t, out.String(), "single file main.conf")
	out.Reset()
	assert.Equal(t, 0, lint([]string{"--project-dir", dir}, strings.NewReader(cfg("sites")), &out), out.String())
}
