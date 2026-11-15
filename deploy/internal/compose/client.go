package compose

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BatMichoo/homeserver/deploy/internal/run"
)

// BaseFile is the project's compose file; OverrideFile (in the state
// directory) pins held services.
const BaseFile = "docker-compose.yaml"

// Client runs docker compose in the checkout.
type Client struct {
	R            run.Runner
	Dir          string // the checkout
	OverrideFile string // .deploy/override.yaml (used when it exists)
}

func (c Client) files() []string {
	args := []string{"-f", filepath.Join(c.Dir, BaseFile)}
	if c.OverrideFile != "" {
		if _, err := os.Stat(c.OverrideFile); err == nil {
			args = append(args, "-f", c.OverrideFile)
		} else if !errors.Is(err, fs.ErrNotExist) {
			// Unreadable override: fail loudly rather than deploy held services.
			args = append(args, "-f", c.OverrideFile)
		}
	}
	return args
}

func (c Client) compose(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	full := append([]string{"compose", "--project-directory", c.Dir}, c.files()...)
	return c.R.Run(ctx, c.Dir, stdin, "docker", append(full, args...)...)
}

// Config is the checkout's resolved configuration, without the override,
// so commits can be compared.
func (c Client) Config(ctx context.Context) (Project, error) {
	out, err := c.R.Run(ctx, c.Dir, nil, "docker", "compose", "--project-directory", c.Dir,
		"-f", filepath.Join(c.Dir, BaseFile), "config", "--format", "json")
	if err != nil {
		return Project{}, err
	}
	return Parse(out, c.Dir)
}

// Build builds a service's image.
func (c Client) Build(ctx context.Context, svc string) error {
	_, err := c.compose(ctx, nil, "build", svc)
	return err
}

// Pull pulls a service's image.
func (c Client) Pull(ctx context.Context, svc string) error {
	_, err := c.compose(ctx, nil, "pull", svc)
	return err
}

// UpWait (re)starts one service and waits for its healthcheck.
func (c Client) UpWait(ctx context.Context, svc string, recreate bool) error {
	args := []string{"up", "-d", "--no-deps", "--no-build", "--wait", "--wait-timeout", "300"}
	if recreate {
		args = append(args, "--force-recreate")
	}
	_, err := c.compose(ctx, nil, append(args, svc)...)
	return err
}

// UpAllWait brings the whole project up and waits until it's healthy.
func (c Client) UpAllWait(ctx context.Context) error {
	_, err := c.compose(ctx, nil, "up", "-d", "--build", "--wait", "--wait-timeout", "300", "--remove-orphans")
	return err
}

// RunOnce runs a one-shot service to completion.
func (c Client) RunOnce(ctx context.Context, svc string) error {
	_, err := c.compose(ctx, nil, "run", "--rm", "--build", "-T", svc)
	return err
}

// Exec runs a command in a running service.
func (c Client) Exec(ctx context.Context, svc string, stdin io.Reader, cmd ...string) ([]byte, error) {
	return c.compose(ctx, stdin, append([]string{"exec", "-T", svc}, cmd...)...)
}

// ImageID is the image of the service's running container ("" if none).
func (c Client) ImageID(ctx context.Context, svc string) (string, error) {
	ids, err := c.containers(ctx, svc)
	if err != nil || len(ids) == 0 {
		return "", err
	}
	img, err := c.R.Run(ctx, c.Dir, nil, "docker", "inspect", "--format", "{{.Image}}", ids[0])
	return strings.TrimSpace(string(img)), err
}

// containers lists a service's containers, running or stopped, by
// compose's labels (so it works for services the current compose file no
// longer has).
func (c Client) containers(ctx context.Context, svc string) ([]string, error) {
	p, err := c.Config(ctx)
	if err != nil {
		return nil, err
	}
	out, err := c.R.Run(ctx, c.Dir, nil, "docker", "ps", "-aq",
		"--filter", "label=com.docker.compose.project="+p.Name,
		"--filter", "label=com.docker.compose.service="+svc)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

// Running is true when the service has a running container.
func (c Client) Running(ctx context.Context, svc string) (bool, error) {
	out, err := c.compose(ctx, nil, "ps", "-q", "--status", "running", svc)
	return strings.TrimSpace(string(out)) != "", err
}

// Remove stops and removes a service's containers, also when the compose
// file no longer has the service (compose itself refuses that).
func (c Client) Remove(ctx context.Context, svc string) error {
	ids, err := c.containers(ctx, svc)
	if err != nil || len(ids) == 0 {
		return err
	}
	_, err = c.R.Run(ctx, c.Dir, nil, "docker", append([]string{"rm", "-f"}, ids...)...)
	return err
}

// PrepareAll builds and pulls every image, without starting anything, so
// a registry or network failure shows up before anything changes.
func (c Client) PrepareAll(ctx context.Context) error {
	if _, err := c.compose(ctx, nil, "build"); err != nil {
		return err
	}
	_, err := c.compose(ctx, nil, "pull", "--ignore-buildable")
	return err
}

// PruneImages deletes dangling images left by deploys (held images are
// tagged, so they're kept).
func (c Client) PruneImages(ctx context.Context) error {
	_, err := c.R.Run(ctx, c.Dir, nil, "docker", "image", "prune", "-f")
	return err
}

// Tag gives an image an extra name (a held service's image, so pruning
// can't delete it).
func (c Client) Tag(ctx context.Context, image, ref string) error {
	_, err := c.R.Run(ctx, c.Dir, nil, "docker", "tag", image, ref)
	return err
}

// Untag removes a name added by Tag; the image itself stays while
// anything uses it.
func (c Client) Untag(ctx context.Context, ref string) error {
	_, err := c.R.Run(ctx, c.Dir, nil, "docker", "image", "rm", "--no-prune", ref)
	return err
}
