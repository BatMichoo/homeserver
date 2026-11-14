package deployer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BatMichoo/homeserver/deploy/internal/ci"
	"github.com/BatMichoo/homeserver/deploy/internal/compose"
	"github.com/BatMichoo/homeserver/deploy/internal/config"
	"github.com/BatMichoo/homeserver/deploy/internal/state"
	"github.com/stretchr/testify/require"
)

// commit is one commit in the fake history.
type commit struct {
	parent  string
	files   []string // changed since parent
	subject string
	cfg     compose.Project
}

type fakeGit struct {
	commits    map[string]commit
	head       string
	remote     string
	branch     string
	dirty      bool
	resetFails bool
	calls      []string
}

func (g *fakeGit) Branch(context.Context) (string, error) { return g.branch, nil }
func (g *fakeGit) Head(context.Context) (string, error)   { return g.head, nil }
func (g *fakeGit) Dirty(context.Context) (bool, error)    { return g.dirty, nil }
func (g *fakeGit) Fetch(ctx context.Context, remote, branch string) (string, error) {
	return g.remote, nil
}
func (g *fakeGit) ancestors(sha string) []string {
	var out []string
	for sha != "" {
		out = append(out, sha)
		sha = g.commits[sha].parent
	}
	return out
}
func (g *fakeGit) IsAncestor(ctx context.Context, a, b string) (bool, error) {
	for _, s := range g.ancestors(b) {
		if s == a {
			return true, nil
		}
	}
	return false, nil
}
func (g *fakeGit) ChangedFiles(ctx context.Context, a, b string) ([]string, error) {
	var files []string
	for _, s := range g.ancestors(b) {
		if s == a {
			return files, nil
		}
		files = append(files, g.commits[s].files...)
	}
	return nil, fmt.Errorf("%s not an ancestor of %s", a, b)
}
func (g *fakeGit) FastForward(ctx context.Context, sha string) error {
	g.calls = append(g.calls, "ff "+sha)
	g.head = sha
	return nil
}
func (g *fakeGit) ResetKeep(ctx context.Context, sha string) error {
	g.calls = append(g.calls, "reset "+sha)
	if g.resetFails {
		return errors.New("reset failed")
	}
	g.head = sha
	return nil
}
func (g *fakeGit) RemoveStray(ctx context.Context, paths []string) error {
	g.calls = append(g.calls, "clean "+strings.Join(paths, ","))
	return nil
}
func (g *fakeGit) SubmoduleUpdate(context.Context) error { return nil }
func (g *fakeGit) Subject(ctx context.Context, sha string) (string, error) {
	return g.commits[sha].subject, nil
}

// fakeCompose runs "services": a service is healthy unless marked
// unhealthy for the commit whose definition it would run. A service held
// by the override runs its old version (healthy unless rollbackFails).
type fakeCompose struct {
	git           *fakeGit
	stateDir      string
	unhealthy     map[string]map[string]bool // commit -> service
	rollbackFails map[string]bool
	checkFails    map[string]map[string]bool // commit -> service
	oneShotFails  map[string]bool
	buildFails    map[string]bool   // service -> build/pull fails (registry down)
	images        map[string]string // service -> image of its container (running or stopped)
	stopped       map[string]bool   // service has a container that isn't running
	tags          map[string]string // tag -> image
	calls         []string
}

func (c *fakeCompose) call(s string) { c.calls = append(c.calls, s) }

func (c *fakeCompose) Config(context.Context) (compose.Project, error) {
	return c.git.commits[c.git.head].cfg, nil
}
func (c *fakeCompose) Build(ctx context.Context, svc string) error {
	c.call("build " + svc)
	if c.buildFails[svc] {
		return errors.New("build failed: registry unreachable")
	}
	return nil
}
func (c *fakeCompose) Pull(ctx context.Context, svc string) error {
	c.call("pull " + svc)
	if c.buildFails[svc] {
		return errors.New("pull failed: registry unreachable")
	}
	return nil
}
func (c *fakeCompose) PrepareAll(ctx context.Context) error {
	c.call("prepare-all")
	for name := range c.git.commits[c.git.head].cfg.Services {
		if c.buildFails[name] {
			return errors.New(name + ": registry unreachable")
		}
	}
	return nil
}
func (c *fakeCompose) Tag(ctx context.Context, image, ref string) error {
	c.call("tag " + ref)
	c.tags[ref] = image
	return nil
}
func (c *fakeCompose) Untag(ctx context.Context, ref string) error {
	c.call("untag " + ref)
	delete(c.tags, ref)
	return nil
}

func (c *fakeCompose) pinned(svc string) bool {
	b, err := os.ReadFile(filepath.Join(c.stateDir, "override.yaml"))
	return err == nil && bytes.Contains(b, []byte("\n  "+svc+":\n    "))
}

func (c *fakeCompose) start(svc string) error {
	if c.pinned(svc) {
		if c.rollbackFails[svc] {
			return errors.New(svc + " unhealthy (old version)")
		}
		c.images[svc] = c.images[svc] // unchanged image
		return nil
	}
	if c.unhealthy[c.git.head][svc] {
		return errors.New(svc + " unhealthy")
	}
	c.images[svc] = "img-" + svc + "@" + c.git.head
	return nil
}

func (c *fakeCompose) UpWait(ctx context.Context, svc string, recreate bool) error {
	if err := ctx.Err(); err != nil {
		c.call("up " + svc + " (cancelled)")
		return err
	}
	if recreate {
		c.call("recreate " + svc)
	} else {
		c.call("up " + svc)
	}
	return c.start(svc)
}

func (c *fakeCompose) UpAllWait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		c.call("up-all (cancelled)")
		return err
	}
	c.call("up-all")
	var errs []error
	for name, s := range c.git.commits[c.git.head].cfg.Services {
		if s.LongRunning() {
			errs = append(errs, c.start(name))
		}
	}
	return errors.Join(errs...)
}

func (c *fakeCompose) RunOnce(ctx context.Context, svc string) error {
	c.call("run " + svc)
	if c.oneShotFails[svc] {
		return errors.New(svc + " failed")
	}
	return nil
}

func (c *fakeCompose) Exec(ctx context.Context, svc string, stdin io.Reader, cmd ...string) ([]byte, error) {
	c.call("exec " + svc + " " + strings.Join(cmd, " "))
	check := c.git.commits[c.git.head].cfg.Services[svc].Labels["homeserver.check"]
	if c.checkFails[c.git.head][svc] && check != "" && strings.Join(cmd, " ") == "sh -c "+check {
		return nil, errors.New("check failed")
	}
	return nil, nil
}

func (c *fakeCompose) ImageID(ctx context.Context, svc string) (string, error) {
	return c.images[svc], nil
}
func (c *fakeCompose) Running(ctx context.Context, svc string) (bool, error) {
	return c.images[svc] != "" && !c.stopped[svc], nil
}
func (c *fakeCompose) Remove(ctx context.Context, svc string) error {
	c.call("rm " + svc)
	delete(c.images, svc)
	return nil
}

type fakeCI map[string]ci.Status

func (f fakeCI) Status(ctx context.Context, repo, sha string) ci.Status {
	if s, ok := f[sha]; ok {
		return s
	}
	return ci.Success
}

// world is a deployer over fakes, with commit "c1" deployed.
type world struct {
	t         *testing.T
	git       *fakeGit
	compose   *fakeCompose
	ci        fakeCI
	backups   []string
	backupErr error
	now       time.Time
	d         *Deployer
	cfg       *config.Config
}

var t0 = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

func newWorld(t *testing.T, first compose.Project) *world {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{Repo: "me/hs", Remote: "remote", Branch: "main", CI: "github",
		Interval: time.Minute, BackupKeep: 14, Dir: dir, StateDir: filepath.Join(dir, ".deploy")}
	g := &fakeGit{commits: map[string]commit{"c1": {subject: "first", cfg: first}}, head: "c1", remote: "c1", branch: "main"}
	w := &world{t: t, git: g, ci: fakeCI{}, now: t0, cfg: cfg}
	w.compose = &fakeCompose{git: g, stateDir: cfg.StateDir, unhealthy: map[string]map[string]bool{},
		rollbackFails: map[string]bool{}, checkFails: map[string]map[string]bool{}, oneShotFails: map[string]bool{},
		buildFails: map[string]bool{}, images: map[string]string{}, stopped: map[string]bool{}, tags: map[string]string{}}
	for name, s := range first.Services {
		if s.LongRunning() {
			w.compose.images[name] = "img-" + name + "@c1"
		}
	}
	w.d = w.newDeployer()
	return w
}

func (w *world) newDeployer() *Deployer {
	w.t.Helper()
	d, err := New(Deps{
		Cfg: w.cfg, Git: w.git, Compose: w.compose, CI: w.ci,
		Backup: func(ctx context.Context, p compose.Project, commit string) error {
			w.backups = append(w.backups, commit)
			return w.backupErr
		},
		Expiry: func(ctx context.Context, host, port string) (time.Time, error) {
			return t0.Add(397 * 24 * time.Hour), nil
		},
		Now: func() time.Time { return w.now },
		Log: log.New(io.Discard, "", 0),
	})
	require.NoError(w.t, err)
	return d
}

// push adds a commit on top of the remote.
func (w *world) push(sha, subject string, cfg compose.Project, files ...string) {
	w.git.commits[sha] = commit{parent: w.git.remote, files: files, subject: subject, cfg: cfg}
	w.git.remote = sha
}

func (w *world) state() *state.State {
	w.t.Helper()
	s, err := state.Load(w.cfg.StateDir)
	require.NoError(w.t, err)
	return s
}

func (w *world) override() string {
	b, _ := os.ReadFile(filepath.Join(w.cfg.StateDir, "override.yaml"))
	return string(b)
}

// --- projects ---------------------------------------------------------------

func svc(name string, opts ...func(*compose.Service)) compose.Service {
	s := compose.Service{Name: name, Restart: "unless-stopped", Healthcheck: true, Labels: map[string]string{}}
	for _, o := range opts {
		o(&s)
	}
	if s.Raw == nil {
		b, _ := json.Marshal(map[string]any{"image": name + ":1"})
		s.Raw = b
	}
	return s
}

func built(ctx string) func(*compose.Service) {
	return func(s *compose.Service) { s.BuildContext = ctx }
}
func mounted(src string) func(*compose.Service) {
	return func(s *compose.Service) { s.Mounts = append(s.Mounts, compose.Mount{Type: "bind", Source: src}) }
}
func needs(d ...string) func(*compose.Service) {
	return func(s *compose.Service) { s.DependsOn = d }
}
func labelled(k, v string) func(*compose.Service) {
	return func(s *compose.Service) { s.Labels[k] = v }
}
func rawDef(r string) func(*compose.Service) {
	return func(s *compose.Service) { s.Raw = json.RawMessage(r) }
}
func oneShot(s *compose.Service) { s.Restart = "no"; s.Healthcheck = false }

func project(services ...compose.Service) compose.Project {
	p := compose.Project{Name: "hs", Services: map[string]compose.Service{},
		Networks: json.RawMessage(`{"default":{}}`), Volumes: json.RawMessage(`{}`)}
	for _, s := range services {
		p.Services[s.Name] = s
	}
	return p
}

// stack is a small home server: db, an app built from apps/app (depends
// on db), a web built from web, nginx with mounted sites and reload/check
// labels, and a scheduled certs one-shot.
func stack(mod ...func(map[string]compose.Service)) compose.Project {
	p := project(
		svc("db", labelled("homeserver.backup", "postgres")),
		svc("app", built("apps/app/server"), needs("db"), rawDef(`{"build":{"context":"apps/app/server"}}`)),
		svc("web", built("web"), rawDef(`{"build":{"context":"web"}}`)),
		svc("nginx", mounted("nginx/sites"), needs("app", "certs"),
			labelled("homeserver.check", "nginx -t"), labelled("homeserver.reload", "nginx -s reload"),
			labelled("homeserver.tls", "443")),
		svc("certs", built("certs"), oneShot, labelled("homeserver.every", "720h"), rawDef(`{"build":{"context":"certs"},"restart":"no"}`)),
	)
	for _, m := range mod {
		m(p.Services)
	}
	return p
}

func withDef(name, raw string) func(map[string]compose.Service) {
	return func(m map[string]compose.Service) {
		s := m[name]
		s.Raw = json.RawMessage(raw)
		m[name] = s
	}
}

func without(name string) func(map[string]compose.Service) {
	return func(m map[string]compose.Service) { delete(m, name) }
}

func with(s compose.Service) func(map[string]compose.Service) {
	return func(m map[string]compose.Service) { m[s.Name] = s }
}
