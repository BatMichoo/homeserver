// Command deploy is the home server's deployer.
//
//	deploy [run]                       poll and deploy (see the README)
//	deploy lint --project-dir <dir>    check `docker compose config --format json` on stdin
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/BatMichoo/homeserver/deploy/internal/backup"
	"github.com/BatMichoo/homeserver/deploy/internal/ci"
	"github.com/BatMichoo/homeserver/deploy/internal/compose"
	"github.com/BatMichoo/homeserver/deploy/internal/config"
	"github.com/BatMichoo/homeserver/deploy/internal/deployer"
	"github.com/BatMichoo/homeserver/deploy/internal/git"
	"github.com/BatMichoo/homeserver/deploy/internal/run"
	"github.com/BatMichoo/homeserver/deploy/internal/tlsinfo"
)

func main() {
	args := os.Args[1:]
	switch {
	case len(args) > 0 && args[0] == "lint":
		os.Exit(lint(args[1:], os.Stdin, os.Stdout))
	case len(args) == 0 || args[0] == "run":
		os.Exit(runDeployer())
	}
	fmt.Fprintln(os.Stderr, "usage: deploy [run] | deploy lint --project-dir <dir>")
	os.Exit(2)
}

// runDeployer polls until stopped (or once, with DEPLOY_ONCE=1).
func runDeployer() int {
	logger := log.New(os.Stdout, "deploy: ", log.LstdFlags)
	dir, err := os.Getwd()
	if err != nil {
		logger.Print(err)
		return 1
	}
	cfg, err := config.Load(os.Getenv, dir)
	if err != nil {
		logger.Print(err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	r := run.Exec{}
	// The checkout belongs to the host user; git refuses it otherwise.
	_, _ = r.Run(ctx, dir, nil, "git", "config", "--global", "--add", "safe.directory", dir)
	if err := os.MkdirAll(filepath.Join(cfg.StateDir, "public"), 0o755); err != nil {
		logger.Print(err)
		return 1
	}

	c := compose.Client{R: r, Dir: dir, OverrideFile: filepath.Join(cfg.StateDir, "override.yaml")}
	d, err := deployer.New(deployer.Deps{
		Cfg:     cfg,
		Git:     git.Git{R: r, Dir: dir},
		Compose: c,
		CI:      ci.Client{Token: cfg.GitHubToken, Log: logger},
		Backup: func(ctx context.Context, p compose.Project, commit string) error {
			files, err := backup.Run(ctx, c, p, filepath.Join(dir, "backups"), commit, cfg.BackupKeep, time.Now())
			for _, f := range files {
				logger.Printf("backed up to %s", f)
			}
			return err
		},
		Expiry: tlsinfo.Expiry,
		Now:    time.Now,
		Log:    logger,
	})
	if err != nil {
		logger.Print(err)
		return 1
	}
	logger.Printf("watching %s@%s every %s", cfg.Repo, cfg.Branch, cfg.Interval)
	if err := d.Resume(ctx); err != nil {
		logger.Printf("resume: %v", err)
	}
	for {
		perr := d.Poll(ctx)
		if perr != nil {
			logger.Printf("poll: %v", perr)
		}
		if err := d.Schedule(ctx); err != nil {
			logger.Printf("schedule: %v", err)
		}
		if err := d.Touch(); err != nil {
			logger.Printf("status: %v", err)
		}
		if cfg.Once {
			if perr != nil {
				return 1
			}
			return 0
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(cfg.Interval):
		}
	}
}

// lint reports compose services the deployer can't handle well.
func lint(args []string, in io.Reader, out io.Writer) int {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	dir := fs.String("project-dir", ".", "compose project directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	data, err := io.ReadAll(in)
	if err != nil {
		fmt.Fprintln(out, err)
		return 2
	}
	p, err := compose.Parse(data, *dir)
	if err != nil {
		fmt.Fprintln(out, err)
		return 2
	}
	problems := compose.Lint(p)
	for _, pr := range problems {
		fmt.Fprintln(out, pr)
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}
