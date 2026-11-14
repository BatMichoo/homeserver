// Package config reads the deployer's settings from the environment.
package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the deployer's settings.
type Config struct {
	Repo, Remote, Branch, CI, GitHubToken string
	Interval                              time.Duration
	BackupKeep                            int
	Once                                  bool   // one round, then exit
	Dir                                   string // the checkout
	StateDir                              string // Dir/.deploy
}

// Load reads the settings through getenv; dir is the checkout.
func Load(getenv func(string) string, dir string) (*Config, error) {
	get := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	var errs []error
	c := &Config{
		Repo:        get("DEPLOY_REPO", "BatMichoo/homeserver"),
		Branch:      get("DEPLOY_BRANCH", "main"),
		CI:          get("DEPLOY_CI", "github"),
		GitHubToken: get("GITHUB_TOKEN", ""),
		Once:        get("DEPLOY_ONCE", "") == "1",
		Dir:         dir,
		StateDir:    filepath.Join(dir, ".deploy"),
	}
	c.Remote = get("DEPLOY_REMOTE", "https://github.com/"+c.Repo+".git")

	iv := get("DEPLOY_INTERVAL", "30m")
	d, err := time.ParseDuration(iv)
	if err != nil || d < time.Minute {
		errs = append(errs, fmt.Errorf("DEPLOY_INTERVAL %q: must be a duration of at least 1m", iv))
	}
	c.Interval = d

	keep := get("DEPLOY_BACKUP_KEEP", "14")
	n, err := strconv.Atoi(keep)
	if err != nil || n < 1 {
		errs = append(errs, fmt.Errorf("DEPLOY_BACKUP_KEEP %q: must be a whole number above 0", keep))
	}
	c.BackupKeep = n

	if c.CI != "github" && c.CI != "off" {
		errs = append(errs, fmt.Errorf("DEPLOY_CI %q: must be github or off", c.CI))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}
