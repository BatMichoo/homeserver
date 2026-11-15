package deployer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BatMichoo/homeserver/deploy/internal/compose"
	"github.com/BatMichoo/homeserver/deploy/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// C2: a stop signal during a deploy doesn't abort it halfway: the deploy
// finishes (and is recorded truthfully) before the deployer exits.
func TestPollFinishesWhenStopped(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "web", stack(), "web/main.go")
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	require.NoError(t, w.d.Poll(stopped))
	s := w.state()
	assert.Equal(t, "deployed", s.LastResult)
	assert.False(t, s.IsSkipped("c2"))
	assert.Equal(t, "img-web@c2", w.compose.images["web"])
}

// C3a: a one-shot whose dependency is held isn't run, and isn't moved to
// the "held" profile (its dependents would make the project invalid).
func TestPollHeldDependencyOneShotNotProfiled(t *testing.T) {
	cfg := func(dbImage string) compose.Project {
		return stack(
			withDef("db", `{"image":"`+dbImage+`"}`),
			with(svc("db-setup", oneShot, needs("db"), built("db"), rawDef(`{"build":{"context":"db"},"restart":"no"}`))),
		)
	}
	w := newWorld(t, cfg("db:1"))
	w.push("c2", "db bump and setup change", cfg("db:2"), "db/setup.sh")
	w.compose.unhealthy["c2"] = map[string]bool{"db": true}
	require.NoError(t, w.d.Poll(ctx))
	assert.NotContains(t, w.compose.calls, "run db-setup")
	assert.NotContains(t, w.override(), "profiles")
	assert.NotContains(t, w.state().Held, "db-setup")
	assert.Contains(t, w.state().Held, "db")
}

// C3b: an existing service that isn't running and fails can't be pinned
// to an image, so the commit is rolled back rather than the service
// removed.
func TestPollStoppedServiceFailingIsShared(t *testing.T) {
	w := newWorld(t, stack())
	delete(w.compose.images, "web")
	w.push("c2", "web", stack(), "web/main.go")
	w.compose.unhealthy["c2"] = map[string]bool{"web": true}
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "rolled-back", w.state().LastResult)
	assert.NotContains(t, w.override(), "profiles")
}

// C3c: a failing new service that others depend on can't be left out.
func TestPollNewServiceWithDependentFailsShared(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "media", stack(with(svc("media")), with(svc("worker", needs("media")))))
	w.compose.unhealthy["c2"] = map[string]bool{"media": true}
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "rolled-back", w.state().LastResult)
	assert.Empty(t, w.override())
}

// I1: a held service is redeployed (not reloaded in place) when a later
// commit only changes its mounted files.
func TestPollHeldServiceMountsOnlyRedeploys(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "nginx image", stack(withDef("nginx", `{"image":"nginx:2"}`)))
	w.compose.unhealthy["c2"] = map[string]bool{"nginx": true}
	require.NoError(t, w.d.Poll(ctx))
	require.Contains(t, w.state().Held, "nginx")

	w.compose.calls = nil
	w.push("c3", "site", stack(withDef("nginx", `{"image":"nginx:2"}`)), "nginx/sites/a.conf")
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, []string{"pull nginx", "up nginx", "untag homeserver-held/nginx:c2", "prune"}, w.compose.calls)
	assert.Equal(t, "img-nginx@c3", w.compose.images["nginx"])
}

// I2: a held service that fails again stays on what it was held on.
func TestPollHeldServiceFailsAgainKeepsOriginalPin(t *testing.T) {
	w := newWorld(t, stack())
	original := w.git.commits["c1"].cfg.Services["web"].Raw
	w.push("c2", "web", stack(withDef("web", `{"build":{"context":"web"},"environment":{"V":"2"}}`)), "web/main.go")
	w.compose.unhealthy["c2"] = map[string]bool{"web": true}
	require.NoError(t, w.d.Poll(ctx))

	w.push("c3", "web again", stack(withDef("web", `{"build":{"context":"web"},"environment":{"V":"3"}}`)), "web/main.go")
	w.compose.unhealthy["c3"] = map[string]bool{"web": true}
	require.NoError(t, w.d.Poll(ctx))
	h := w.state().Held["web"]
	assert.JSONEq(t, string(original), string(h.Definition))
	assert.Equal(t, "c3", h.Commit)
	assert.Equal(t, "partial", w.state().LastResult)
}

// I3: if the checkout can't be put back, nothing is brought up on the
// bad commit, and the result says the rollback failed.
func TestPollRollbackResetFails(t *testing.T) {
	w := newWorld(t, stack())
	cfg := stack()
	cfg.Networks = json.RawMessage(`{"new":{}}`)
	w.push("c2", "net", cfg, "web/main.go")
	w.compose.unhealthy["c2"] = map[string]bool{"web": true}
	w.git.resetFails = true
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "rollback-failed", w.state().LastResult)
	assert.Equal(t, 1, countCalls(w.compose.calls, "up-all"), "not brought up again on the bad commit")
}

// I4: stray directories (removed submodules) are cleaned after moving the
// checkout, so it doesn't look modified forever.
func TestPollCleansStrayPaths(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "drop app submodule", stack(without("app")), "apps/app")
	require.NoError(t, w.d.Poll(ctx))
	assert.Contains(t, w.git.calls, "clean apps/app")
}

// I5: nothing runs on schedule while the checkout can't be trusted.
func TestScheduleSkippedWhenBlocked(t *testing.T) {
	w := newWorld(t, stack())
	w.git.dirty = true
	require.NoError(t, w.d.Schedule(ctx))
	assert.Empty(t, w.compose.calls)
	w.git.dirty = false
	w.git.branch = "feature"
	require.NoError(t, w.d.Schedule(ctx))
	assert.Empty(t, w.compose.calls)
}

// I6: a build or pull that fails (registry down) is retried next poll,
// not held for good or skipped.
func TestPollBuildFailureRetried(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "web", stack(), "web/main.go")
	w.compose.buildFails["web"] = true
	require.NoError(t, w.d.Poll(ctx))
	s := w.state()
	assert.Empty(t, s.Held)
	assert.False(t, s.IsSkipped("c2"))
	assert.Equal(t, "c1", w.git.head)
	assert.Equal(t, "build-failed", s.LastResult)

	w.compose.buildFails["web"] = false
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "deployed", w.state().LastResult)
	assert.Equal(t, "img-web@c2", w.compose.images["web"])
}

func TestPollSharedBuildFailureRetried(t *testing.T) {
	w := newWorld(t, stack())
	cfg := stack()
	cfg.Volumes = json.RawMessage(`{"new":{}}`)
	w.push("c2", "vol", cfg, "web/main.go")
	w.compose.buildFails["web"] = true
	require.NoError(t, w.d.Poll(ctx))
	assert.False(t, w.state().IsSkipped("c2"))
	assert.Equal(t, "build-failed", w.state().LastResult)
	assert.NotContains(t, w.compose.calls, "up-all")
}

// I7: a held service is pinned to a tag, so pruning images can't delete
// what it runs; the tag goes when the hold is released.
func TestPollHeldImageTagged(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "web", stack(), "web/main.go")
	w.compose.unhealthy["c2"] = map[string]bool{"web": true}
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "img-web@c1", w.compose.tags["homeserver-held/web:c2"])
	assert.Equal(t, "homeserver-held/web:c2", w.state().Held["web"].Image)
	assert.Contains(t, w.override(), `image: !override "homeserver-held/web:c2"`)

	w.push("c3", "fix", stack(), "web/main.go")
	require.NoError(t, w.d.Poll(ctx))
	assert.Empty(t, w.compose.tags)
}

func countCalls(calls []string, c string) int {
	n := 0
	for _, x := range calls {
		if x == c {
			n++
		}
	}
	return n
}

// Killed during a rollback, after the checkout was put back: the next
// start finishes it (brings everything up, never retries the commit).
func TestResumeDuringRollback(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "web", stack(), "web/main.go")
	s := w.state()
	s.Deploying = &state.Deploying{From: "c1", To: "c2", RollingBack: true}
	require.NoError(t, s.Save(w.cfg.StateDir))
	require.NoError(t, w.newDeployer().Resume(ctx))
	s = w.state()
	assert.True(t, s.IsSkipped("c2"))
	assert.Nil(t, s.Deploying)
	assert.Contains(t, w.compose.calls, "up-all")
}

func TestPollPrunesAfterDeploy(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "web", stack(), "web/main.go")
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "prune", w.compose.calls[len(w.compose.calls)-1])

	w.compose.calls = nil
	w.push("c3", "docs", stack(), "README.md")
	require.NoError(t, w.d.Poll(ctx))
	assert.NotContains(t, w.compose.calls, "prune", "nothing built, nothing to prune")
}

func TestPollComposeFileChangedMayOutdateDeployer(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "compose", stack(), "docker-compose.yaml")
	require.NoError(t, w.d.Poll(ctx))
	var kinds []string
	for _, e := range w.state().Events {
		kinds = append(kinds, e.Kind)
	}
	assert.Contains(t, kinds, "deployer-outdated")
}

func TestStatusUsesDeployerClock(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "docs", stack(), "README.md")
	require.NoError(t, w.d.Poll(ctx))
	b, err := os.ReadFile(filepath.Join(w.cfg.StateDir, "public", "status.json"))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"updated_at": "2026-11-15T10:00:00Z"`)
}

// Touch refreshes status.json each poll, so the dashboard can tell a
// deployer that stopped.
func TestTouchWritesStatus(t *testing.T) {
	w := newWorld(t, stack())
	w.now = t0.Add(time.Hour)
	require.NoError(t, w.d.Touch())
	b, err := os.ReadFile(filepath.Join(w.cfg.StateDir, "public", "status.json"))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"updated_at": "2026-11-15T11:00:00Z"`)
}

// A lock keeps a manual make up from running in the middle of a deploy.
func TestPollHoldsLock(t *testing.T) {
	w := newWorld(t, stack())
	unlock, err := state.Lock(w.cfg.StateDir, time.Second)
	require.NoError(t, err)
	w.d.lockWait = 100 * time.Millisecond
	w.push("c2", "web", stack(), "web/main.go")
	assert.Error(t, w.d.Poll(ctx), "someone else holds the lock")
	assert.Equal(t, "c1", w.git.head)
	unlock()
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "c2", w.git.head)
}
