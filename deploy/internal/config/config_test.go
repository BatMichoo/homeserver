package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(nil), "/srv/homeserver")
	require.NoError(t, err)
	assert.Equal(t, "BatMichoo/homeserver", c.Repo)
	assert.Equal(t, "https://github.com/BatMichoo/homeserver.git", c.Remote)
	assert.Equal(t, "main", c.Branch)
	assert.Equal(t, 30*time.Minute, c.Interval)
	assert.Equal(t, 14, c.BackupKeep)
	assert.Equal(t, "github", c.CI)
	assert.False(t, c.Once)
	assert.Equal(t, "/srv/homeserver", c.Dir)
	assert.Equal(t, "/srv/homeserver/.deploy", c.StateDir)

	c, err = Load(env(map[string]string{"DEPLOY_REPO": "me/x", "DEPLOY_ONCE": "1", "GITHUB_TOKEN": "t"}), "/d")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/me/x.git", c.Remote)
	assert.True(t, c.Once)
	assert.Equal(t, "t", c.GitHubToken)
}

func TestLoadInvalid(t *testing.T) {
	for k, v := range map[string]string{
		"DEPLOY_INTERVAL":    "10s",
		"DEPLOY_BACKUP_KEEP": "0",
		"DEPLOY_CI":          "nope",
	} {
		_, err := Load(env(map[string]string{k: v}), "/d")
		assert.ErrorContains(t, err, k)
	}
}
