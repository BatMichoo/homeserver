package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func full() map[string]string {
	return map[string]string{
		"DB_HOST": "db", "DB_NAME": "auth", "DB_USER": "auth", "DB_PASS": "pw",
		"LAN_DOMAIN": "homeserver.lan",
	}
}

func TestLoadRequired(t *testing.T) {
	_, err := Load(env(nil))
	require.Error(t, err)
	for _, k := range []string{"DB_HOST", "DB_NAME", "DB_USER", "DB_PASS", "LAN_DOMAIN"} {
		assert.Contains(t, err.Error(), k)
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(full()))
	require.NoError(t, err)
	assert.Equal(t, 5432, c.DB.Port)
	assert.Equal(t, "disable", c.DB.SSLMode)
	assert.Equal(t, 443, c.HTTPSPort)
	assert.Equal(t, 720*time.Hour, c.SessionTTL)
	assert.Equal(t, ":8080", c.Addr)
	assert.Equal(t, "homeserver.lan", c.LanDomain)
}

func TestSessionTTLMinimum(t *testing.T) {
	m := full()
	m["SESSION_TTL"] = "30m"
	_, err := Load(env(m))
	assert.ErrorContains(t, err, "SESSION_TTL")
	m["SESSION_TTL"] = "nonsense"
	_, err = Load(env(m))
	assert.ErrorContains(t, err, "SESSION_TTL")
	m["SESSION_TTL"] = "48h"
	c, err := Load(env(m))
	require.NoError(t, err)
	assert.Equal(t, 48*time.Hour, c.SessionTTL)
}

func TestBadPorts(t *testing.T) {
	for _, k := range []string{"DB_PORT", "HTTPS_PORT"} {
		m := full()
		m[k] = "x"
		_, err := Load(env(m))
		assert.ErrorContains(t, err, k)
	}
}

func TestPublicURL(t *testing.T) {
	c, err := Load(env(full()))
	require.NoError(t, err)
	assert.Equal(t, "https://auth.homeserver.lan", c.PublicURL())
	c.HTTPSPort = 18443
	assert.Equal(t, "https://auth.homeserver.lan:18443", c.PublicURL())
}

func TestHomeServerURL(t *testing.T) {
	c, err := Load(env(full()))
	require.NoError(t, err)
	assert.Equal(t, "https://homeserver.lan/", c.HomeServerURL())
	c.HTTPSPort = 18443
	assert.Equal(t, "https://homeserver.lan:18443/", c.HomeServerURL())
	c.LanDomain = ""
	assert.Equal(t, "", c.HomeServerURL(), "no domain: no link")
}

func TestDSN(t *testing.T) {
	c, err := Load(env(full()))
	require.NoError(t, err)
	assert.Equal(t, "host=db port=5432 user=auth password='pw' dbname=auth sslmode=disable", c.DB.DSN())
}
