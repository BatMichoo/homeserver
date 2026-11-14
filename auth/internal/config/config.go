// Package config reads the auth service's settings from the environment.
// Only this package reads the environment.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DBConfig is how to reach the auth database.
type DBConfig struct {
	Host, Name, User, Pass, SSLMode string
	Port                            int
}

// DSN is a lib/pq connection string.
func (d DBConfig) DSN() string {
	pass := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(d.Pass)
	return fmt.Sprintf("host=%s port=%d user=%s password='%s' dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, pass, d.Name, d.SSLMode)
}

// Config is everything the service needs.
type Config struct {
	DB         DBConfig
	LanDomain  string        // e.g. homeserver.lan; cookies are set for it
	HTTPSPort  int           // where nginx serves HTTPS (443 unless changed)
	SessionTTL time.Duration // how long a session lasts without use
	Addr       string        // where to listen, e.g. :8080
}

// PublicURL is where people reach this service, e.g. https://auth.homeserver.lan.
func (c *Config) PublicURL() string {
	u := "https://auth." + c.LanDomain
	if c.HTTPSPort != 443 {
		u += ":" + strconv.Itoa(c.HTTPSPort)
	}
	return u
}

// Load reads the settings through getenv (os.Getenv in main).
func Load(getenv func(string) string) (*Config, error) {
	var errs []error
	required := func(k string) string {
		v := strings.TrimSpace(getenv(k))
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is not set", k))
		}
		return v
	}
	orDefault := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	port := func(k, def string) int {
		v := orDefault(k, def)
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			errs = append(errs, fmt.Errorf("%s %q: must be a port number", k, v))
		}
		return n
	}

	c := &Config{
		DB: DBConfig{
			Host:    required("DB_HOST"),
			Port:    port("DB_PORT", "5432"),
			Name:    required("DB_NAME"),
			User:    required("DB_USER"),
			Pass:    required("DB_PASS"),
			SSLMode: orDefault("DB_SSLMODE", "disable"),
		},
		LanDomain: required("LAN_DOMAIN"),
		HTTPSPort: port("HTTPS_PORT", "443"),
		Addr:      orDefault("ADDR", ":8080"),
	}
	ttl := orDefault("SESSION_TTL", "720h")
	d, err := time.ParseDuration(ttl)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("SESSION_TTL %q: must be a duration like 720h", ttl))
	case d < time.Hour:
		errs = append(errs, fmt.Errorf("SESSION_TTL %q: must be at least 1h", ttl))
	}
	c.SessionTTL = d

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}
