package deployer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BatMichoo/homeserver/deploy/internal/ci"
	"github.com/BatMichoo/homeserver/deploy/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

func lastEvent(s *state.State) state.Event {
	if len(s.Events) == 0 {
		return state.Event{}
	}
	return s.Events[len(s.Events)-1]
}

func TestPollDocsOnly(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "docs", stack(), "README.md")
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "c2", w.git.head)
	assert.Empty(t, w.compose.calls, "nothing rebuilt or restarted")
	s := w.state()
	assert.Equal(t, "deployed", s.LastResult)
	assert.Equal(t, "c2", s.Deployed.Commit)
	assert.Equal(t, "docs", s.Deployed.Subject)
	assert.Nil(t, s.Deploying)
	assert.Equal(t, []string{"c2"}, w.backups, "backed up before deploying")
}

func TestPollNothingNew(t *testing.T) {
	w := newWorld(t, stack())
	require.NoError(t, w.d.Poll(ctx))
	assert.Empty(t, w.git.calls)
	assert.Empty(t, w.backups)
}

func TestPollCIPendingWaits(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "x", stack(), "web/a.go")
	w.ci["c2"] = ci.Pending
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "c1", w.git.head)
	assert.False(t, w.state().IsSkipped("c2"))
	assert.Empty(t, w.backups)
}

func TestPollCIFailedSkips(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "x", stack(), "web/a.go")
	w.ci["c2"] = ci.Failure
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "c1", w.git.head)
	s := w.state()
	assert.True(t, s.IsSkipped("c2"))
	assert.Equal(t, "ci-failed", lastEvent(s).Kind)
	w.ci["c2"] = ci.Success
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "c1", w.git.head, "never retried")
}

func TestPollCIOff(t *testing.T) {
	w := newWorld(t, stack())
	w.cfg.CI = "off"
	w.push("c2", "x", stack(), "README.md")
	w.ci["c2"] = ci.Failure
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "c2", w.git.head)
}

func TestPollBlockedDirty(t *testing.T) {
	w := newWorld(t, stack())
	w.git.dirty = true
	w.push("c2", "x", stack(), "web/a.go")
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "c1", w.git.head)
	assert.Equal(t, "blocked", lastEvent(w.state()).Kind)
	require.NoError(t, w.d.Poll(ctx))
	assert.Len(t, w.state().Events, 1, "same block isn't repeated every poll")
}

func TestPollBlockedWrongBranch(t *testing.T) {
	w := newWorld(t, stack())
	w.git.branch = "feature"
	w.push("c2", "x", stack(), "web/a.go")
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "c1", w.git.head)
	assert.Equal(t, "blocked", lastEvent(w.state()).Kind)
}

func TestPollBlockedDiverged(t *testing.T) {
	w := newWorld(t, stack())
	w.git.commits["local"] = commit{parent: "c1", cfg: stack()}
	w.git.head = "local"
	w.push("c2", "x", stack(), "web/a.go")
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "local", w.git.head)
	assert.Equal(t, "blocked", lastEvent(w.state()).Kind)
}

func TestPollBackupFailureStops(t *testing.T) {
	w := newWorld(t, stack())
	w.backupErr = assert.AnError
	w.push("c2", "x", stack(), "web/a.go")
	assert.Error(t, w.d.Poll(ctx))
	assert.Equal(t, "c1", w.git.head)
	s := w.state()
	assert.Equal(t, "backup-failed", lastEvent(s).Kind)
	assert.False(t, s.IsSkipped("c2"), "retried next poll")
	assert.Nil(t, s.Deploying)
}

func TestPollOneServiceChanged(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "web change", stack(), "web/main.go")
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, []string{"build web", "up web"}, w.compose.calls)
	assert.Equal(t, "img-web@c2", w.compose.images["web"])
	assert.Equal(t, "img-app@c1", w.compose.images["app"])
	assert.Equal(t, "deployed", w.state().LastResult)
}

func TestPollSubmoduleBump(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "bump app", stack(), "apps/app")
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, []string{"build app", "up app"}, w.compose.calls)
}

func TestPollImagePinPulls(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "db", stack(withDef("db", `{"image":"db:2"}`)))
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, []string{"pull db", "up db"}, w.compose.calls)
}

func TestPollUnhealthyServiceHeld(t *testing.T) {
	w := newWorld(t, stack())
	newCfg := stack(withDef("web", `{"build":{"context":"web"},"environment":{"X":"bad"}}`))
	w.push("c2", "web and app", newCfg, "web/main.go", "apps/app")
	w.compose.unhealthy["c2"] = map[string]bool{"web": true}
	require.NoError(t, w.d.Poll(ctx))

	assert.Equal(t, "img-app@c2", w.compose.images["app"], "the healthy change is deployed")
	assert.Equal(t, "img-web@c1", w.compose.images["web"], "web back on its old image")
	assert.Equal(t, "c2", w.git.head, "the checkout stays on the new commit")
	s := w.state()
	assert.Equal(t, "partial", s.LastResult)
	h := s.Held["web"]
	assert.Equal(t, "c2", h.Commit)
	assert.Equal(t, "img-web@c1", h.Image)
	assert.JSONEq(t, `{"build":{"context":"web"}}`, string(h.Definition), "the old definition")
	assert.Contains(t, w.override(), "  web:\n")
	assert.Contains(t, w.override(), `environment: !reset null`, "the new key is undone")
	assert.False(t, s.IsSkipped("c2"), "a partial deploy isn't a skipped commit")
}

func TestPollDependentsOfHeldAreHeld(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "db and app", stack(withDef("db", `{"image":"db:2"}`)), "apps/app")
	w.compose.unhealthy["c2"] = map[string]bool{"db": true}
	require.NoError(t, w.d.Poll(ctx))
	s := w.state()
	assert.Contains(t, s.Held, "db")
	assert.Contains(t, s.Held, "app")
	assert.Equal(t, "dependency held", s.Held["app"].Reason)
	assert.NotContains(t, w.compose.calls, "build app", "a dependent of a held service isn't deployed")
	assert.Equal(t, "img-app@c1", w.compose.images["app"])
}

func TestPollRollbackUnhealthyGoesShared(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "web", stack(), "web/main.go")
	w.compose.unhealthy["c2"] = map[string]bool{"web": true}
	w.compose.rollbackFails["web"] = true
	require.NoError(t, w.d.Poll(ctx))
	s := w.state()
	assert.Equal(t, "rolled-back", s.LastResult)
	assert.Equal(t, "c1", w.git.head)
	assert.True(t, s.IsSkipped("c2"))
	assert.Empty(t, s.Held)
	assert.Empty(t, w.override())
	assert.Contains(t, w.compose.calls, "up-all")
}

func TestPollMountsOnlyReloads(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "site", stack(), "nginx/sites/a.conf")
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, []string{"exec nginx sh -c nginx -t", "exec nginx sh -c nginx -s reload"}, w.compose.calls)
	assert.Equal(t, "deployed", w.state().LastResult)
}

func TestPollMountsOnlyCheckFails(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "bad site", stack(), "nginx/sites/a.conf")
	w.compose.checkFails["c2"] = map[string]bool{"nginx": true}
	require.NoError(t, w.d.Poll(ctx))
	s := w.state()
	assert.Equal(t, "rolled-back", s.LastResult)
	assert.Equal(t, "c1", w.git.head)
	assert.NotContains(t, w.compose.calls, "exec nginx sh -c nginx -s reload")
}

func TestPollMountsOnlyWithoutReloadRecreates(t *testing.T) {
	cfg := stack(with(svc("files", mounted("files"))))
	w := newWorld(t, cfg)
	w.push("c2", "files", cfg, "files/x")
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, []string{"recreate files"}, w.compose.calls)
}

func TestPollNewServiceFailsRemoved(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "media", stack(with(svc("media"))))
	w.compose.unhealthy["c2"] = map[string]bool{"media": true}
	require.NoError(t, w.d.Poll(ctx))
	assert.Contains(t, w.compose.calls, "rm media")
	s := w.state()
	assert.Equal(t, "partial", s.LastResult)
	assert.Equal(t, "", s.Held["media"].Image)
	assert.Contains(t, w.override(), "  media:\n    profiles: !override [\"held\"]\n")
}

func TestPollNewServiceDeployed(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "media", stack(with(svc("media"))))
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, []string{"pull media", "up media"}, w.compose.calls)
}

func TestPollRemovedService(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "drop web", stack(without("web")))
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, []string{"rm web"}, w.compose.calls)
}

func TestPollSharedFailureRollsBack(t *testing.T) {
	w := newWorld(t, stack())
	cfg := stack()
	cfg.Networks = json.RawMessage(`{"default":{},"new":{}}`)
	w.push("c2", "network", cfg, "web/main.go")
	w.compose.unhealthy["c2"] = map[string]bool{"web": true}
	require.NoError(t, w.d.Poll(ctx))
	s := w.state()
	assert.Equal(t, "rolled-back", s.LastResult)
	assert.Equal(t, "c1", w.git.head)
	assert.True(t, s.IsSkipped("c2"))
	assert.Equal(t, "rolled-back", lastEvent(s).Kind)
}

func TestPollSharedSuccess(t *testing.T) {
	w := newWorld(t, stack())
	cfg := stack()
	cfg.Volumes = json.RawMessage(`{"new":{}}`)
	w.push("c2", "volume", cfg)
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, []string{"up-all"}, w.compose.calls)
	assert.Equal(t, "deployed", w.state().LastResult)
}

func TestPollOneShotRunsAndFailureIsShared(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "certs", stack(), "certs/issue.sh")
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, []string{"run certs"}, w.compose.calls)

	w.push("c3", "certs again", stack(), "certs/issue.sh")
	w.compose.oneShotFails["certs"] = true
	require.NoError(t, w.d.Poll(ctx))
	assert.Equal(t, "rolled-back", w.state().LastResult)
	assert.Equal(t, "c2", w.git.head)
}

func TestPollHeldServiceKeptThenRetried(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "web", stack(), "web/main.go")
	w.compose.unhealthy["c2"] = map[string]bool{"web": true}
	require.NoError(t, w.d.Poll(ctx))
	require.Contains(t, w.state().Held, "web")

	w.push("c3", "app only", stack(), "apps/app")
	require.NoError(t, w.d.Poll(ctx))
	assert.Contains(t, w.state().Held, "web", "still held")
	assert.Contains(t, w.override(), "  web:\n")

	w.push("c4", "fix web", stack(), "web/main.go")
	require.NoError(t, w.d.Poll(ctx))
	s := w.state()
	assert.NotContains(t, s.Held, "web")
	assert.Empty(t, w.override(), "override removed")
	assert.Equal(t, "img-web@c4", w.compose.images["web"])
	assert.Equal(t, "deployed", s.LastResult)
}

func TestPollHeldServiceRemoved(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "web", stack(), "web/main.go")
	w.compose.unhealthy["c2"] = map[string]bool{"web": true}
	require.NoError(t, w.d.Poll(ctx))
	w.push("c3", "drop web", stack(without("web")))
	require.NoError(t, w.d.Poll(ctx))
	assert.NotContains(t, w.state().Held, "web")
	assert.Empty(t, w.override())
	assert.Contains(t, w.compose.calls, "rm web")
}

func TestPollDeployerOutdated(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "deployer", stack(), "deploy/internal/x.go")
	require.NoError(t, w.d.Poll(ctx))
	var kinds []string
	for _, e := range w.state().Events {
		kinds = append(kinds, e.Kind)
	}
	assert.Contains(t, kinds, "deployer-outdated")
}

func TestResumeInterruptedDeploy(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "web", stack(), "web/main.go")
	// A deploy of c2 was cut short after the fast-forward.
	s := w.state()
	s.Deploying = &state.Deploying{From: "c1", To: "c2"}
	require.NoError(t, s.Save(w.cfg.StateDir))
	w.git.head = "c2"

	d := w.newDeployer()
	require.NoError(t, d.Resume(ctx))
	s = w.state()
	assert.Equal(t, "c1", w.git.head)
	assert.True(t, s.IsSkipped("c2"))
	assert.Nil(t, s.Deploying)
	var kinds []string
	for _, e := range s.Events {
		kinds = append(kinds, e.Kind)
	}
	assert.Contains(t, kinds, "resumed")
}

func TestResumeBeforeFastForward(t *testing.T) {
	w := newWorld(t, stack())
	s := w.state()
	s.Deploying = &state.Deploying{From: "c1", To: "c2"}
	require.NoError(t, s.Save(w.cfg.StateDir))
	require.NoError(t, w.newDeployer().Resume(ctx))
	assert.Nil(t, w.state().Deploying)
	assert.False(t, w.state().IsSkipped("c2"), "never started, so it may be tried")
	assert.Empty(t, w.compose.calls)
}

func TestScheduleRunsDueAndReloadsDependents(t *testing.T) {
	w := newWorld(t, stack())
	require.NoError(t, w.d.Schedule(ctx))
	assert.Equal(t, []string{"run certs", "exec nginx sh -c nginx -s reload"}, w.compose.calls)
	s := w.state()
	assert.Equal(t, t0, s.LastRun["certs"])
	assert.Equal(t, "scheduled-ok", lastEvent(s).Kind)
	assert.Equal(t, t0.Add(397*24*time.Hour), s.CertExpiry)

	w.compose.calls = nil
	w.now = t0.Add(719 * time.Hour)
	require.NoError(t, w.d.Schedule(ctx))
	assert.Empty(t, w.compose.calls, "not due yet")
	w.now = t0.Add(720 * time.Hour)
	require.NoError(t, w.d.Schedule(ctx))
	assert.Equal(t, []string{"run certs", "exec nginx sh -c nginx -s reload"}, w.compose.calls)
}

func TestScheduleFailureRetried(t *testing.T) {
	w := newWorld(t, stack())
	w.compose.oneShotFails["certs"] = true
	require.NoError(t, w.d.Schedule(ctx))
	s := w.state()
	assert.Equal(t, "scheduled-failed", lastEvent(s).Kind)
	assert.True(t, s.LastRun["certs"].IsZero())
	w.compose.oneShotFails["certs"] = false
	w.compose.calls = nil
	require.NoError(t, w.d.Schedule(ctx))
	assert.Contains(t, w.compose.calls, "run certs")
}

func TestStatusWrittenAfterPoll(t *testing.T) {
	w := newWorld(t, stack())
	w.push("c2", "web", stack(), "web/main.go")
	w.compose.unhealthy["c2"] = map[string]bool{"web": true}
	require.NoError(t, w.d.Poll(ctx))
	b, err := os.ReadFile(filepath.Join(w.cfg.StateDir, "public", "status.json"))
	require.NoError(t, err)
	var st map[string]any
	require.NoError(t, json.Unmarshal(b, &st))
	assert.Equal(t, "partial", st["last_result"])
	assert.Len(t, st["held"], 1)
	assert.NotNil(t, st["cert_expiry"])
}
