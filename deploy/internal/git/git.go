// Package git is the deployer's view of the checkout.
package git

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/BatMichoo/homeserver/deploy/internal/run"
)

// Git runs git in the checkout Dir.
type Git struct {
	R   run.Runner
	Dir string
}

func (g Git) git(ctx context.Context, args ...string) (string, error) {
	out, err := g.R.Run(ctx, g.Dir, nil, "git", args...)
	return strings.TrimSpace(string(out)), err
}

// Branch is the checked-out branch ("HEAD" when detached).
func (g Git) Branch(ctx context.Context) (string, error) {
	return g.git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
}

// Head is the checked-out commit.
func (g Git) Head(ctx context.Context) (string, error) {
	return g.git(ctx, "rev-parse", "HEAD")
}

// Dirty is true when the checkout, submodules included, has local changes
// or untracked files. Ignored files (.deploy/, backups/) don't count.
func (g Git) Dirty(ctx context.Context) (bool, error) {
	out, err := g.git(ctx, "status", "--porcelain", "--ignore-submodules=none")
	return out != "", err
}

// Fetch fetches branch from remote and returns the fetched commit.
func (g Git) Fetch(ctx context.Context, remote, branch string) (string, error) {
	if _, err := g.git(ctx, "fetch", "--quiet", remote, branch); err != nil {
		return "", err
	}
	return g.git(ctx, "rev-parse", "FETCH_HEAD")
}

// IsAncestor is true when a is b or an ancestor of it.
func (g Git) IsAncestor(ctx context.Context, a, b string) (bool, error) {
	_, err := g.git(ctx, "merge-base", "--is-ancestor", a, b)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// ChangedFiles lists the paths changed between a and b. A submodule bump
// shows as the submodule's path.
func (g Git) ChangedFiles(ctx context.Context, a, b string) ([]string, error) {
	out, err := g.git(ctx, "diff", "--name-only", a, b)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// FastForward moves the checkout to sha (fails unless it's a fast-forward).
func (g Git) FastForward(ctx context.Context, sha string) error {
	_, err := g.git(ctx, "merge", "--ff-only", "--quiet", sha)
	return err
}

// ResetKeep moves the checkout back to sha, keeping no local changes.
func (g Git) ResetKeep(ctx context.Context, sha string) error {
	_, err := g.git(ctx, "reset", "--keep", sha)
	return err
}

// SubmoduleUpdate checks out the submodule commits the checkout pins.
func (g Git) SubmoduleUpdate(ctx context.Context) error {
	_, err := g.git(ctx, "submodule", "update", "--init", "--recursive")
	return err
}

// RemoveStray deletes paths among the given ones that are directories git
// no longer tracks, such as a removed submodule's leftover checkout
// (`git merge` and `git reset` leave those behind). Tracked paths and
// ignored files are never touched.
func (g Git) RemoveStray(ctx context.Context, paths []string) error {
	for _, p := range paths {
		if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
			continue
		}
		if tracked, _ := g.git(ctx, "ls-files", "--", p); tracked != "" {
			continue
		}
		if _, err := g.git(ctx, "clean", "-ffdq", "--", p); err != nil {
			return err
		}
	}
	return nil
}

// Subject is a commit's first line.
func (g Git) Subject(ctx context.Context, sha string) (string, error) {
	return g.git(ctx, "log", "-1", "--format=%s", sha)
}
