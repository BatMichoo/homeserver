// Package deployer deploys the checkout's branch: per service where it
// can, the whole commit where it must. It knows no service by name; the
// compose file's labels say what's special.
package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/BatMichoo/homeserver/deploy/internal/ci"
	"github.com/BatMichoo/homeserver/deploy/internal/compose"
	"github.com/BatMichoo/homeserver/deploy/internal/config"
	"github.com/BatMichoo/homeserver/deploy/internal/plan"
	"github.com/BatMichoo/homeserver/deploy/internal/state"
)

// Labels the deployer reads.
const (
	LabelCheck  = "homeserver.check"  // command to validate a config change before applying it
	LabelReload = "homeserver.reload" // command to apply a mounted-file change in place
	LabelEvery  = "homeserver.every"  // run this one-shot this often
	LabelTLS    = "homeserver.tls"    // read the certificate expiry from this port
)

// Git is the checkout.
type Git interface {
	Branch(ctx context.Context) (string, error)
	Head(ctx context.Context) (string, error)
	Dirty(ctx context.Context) (bool, error)
	Fetch(ctx context.Context, remote, branch string) (string, error)
	IsAncestor(ctx context.Context, a, b string) (bool, error)
	ChangedFiles(ctx context.Context, a, b string) ([]string, error)
	FastForward(ctx context.Context, sha string) error
	ResetKeep(ctx context.Context, sha string) error
	SubmoduleUpdate(ctx context.Context) error
	Subject(ctx context.Context, sha string) (string, error)
}

// Compose runs the project.
type Compose interface {
	Config(ctx context.Context) (compose.Project, error)
	Build(ctx context.Context, svc string) error
	Pull(ctx context.Context, svc string) error
	UpWait(ctx context.Context, svc string, recreate bool) error
	UpAllWait(ctx context.Context) error
	RunOnce(ctx context.Context, svc string) error
	Exec(ctx context.Context, svc string, stdin io.Reader, cmd ...string) ([]byte, error)
	ImageID(ctx context.Context, svc string) (string, error)
	Running(ctx context.Context, svc string) (bool, error)
	Remove(ctx context.Context, svc string) error
}

// CI reports a commit's checks.
type CI interface {
	Status(ctx context.Context, repo, sha string) ci.Status
}

// Deps is everything the deployer uses.
type Deps struct {
	Cfg     *config.Config
	Git     Git
	Compose Compose
	CI      CI
	Backup  func(ctx context.Context, p compose.Project, commit string) error
	Expiry  func(ctx context.Context, host, port string) (time.Time, error)
	Now     func() time.Time
	Log     *log.Logger
}

// Deployer deploys commits.
type Deployer struct {
	Deps
	st *state.State
}

// New loads the state.
func New(d Deps) (*Deployer, error) {
	st, err := state.Load(d.Cfg.StateDir)
	if err != nil {
		return nil, err
	}
	if st.Held == nil {
		st.Held = map[string]state.Held{}
	}
	if st.LastRun == nil {
		st.LastRun = map[string]time.Time{}
	}
	return &Deployer{Deps: d, st: st}, nil
}

func (d *Deployer) event(kind, commit, service, msg string) {
	d.Log.Printf("%s %s %s: %s", kind, short(commit), service, msg)
	d.st.Add(state.Event{Time: d.Now(), Kind: kind, Commit: commit, Service: service, Message: msg})
}

func (d *Deployer) save() error {
	if err := d.st.Save(d.Cfg.StateDir); err != nil {
		return err
	}
	return state.WriteStatus(d.Cfg.StateDir, d.st)
}

// blocked records why nothing can be deployed, once (not every poll).
func (d *Deployer) blocked(msg string) error {
	if n := len(d.st.Events); n > 0 && d.st.Events[n-1].Kind == "blocked" && d.st.Events[n-1].Message == msg {
		return nil
	}
	d.st.LastResult = "blocked"
	d.event("blocked", "", "", msg)
	return d.save()
}

// Resume finishes a deploy that was cut short: if the checkout already
// moved to the new commit, it's rolled back as a failed shared deploy.
func (d *Deployer) Resume(ctx context.Context) error {
	dep := d.st.Deploying
	if dep == nil {
		return nil
	}
	head, err := d.Git.Head(ctx)
	if err != nil {
		return err
	}
	if head != dep.To {
		d.st.Deploying = nil
		return d.save()
	}
	d.event("resumed", dep.To, "", "a deploy was interrupted; rolling it back")
	return d.sharedRollback(ctx, dep.From, dep.To, d.st.Held, "the deploy was interrupted")
}

// Poll deploys the remote branch's newest commit, if it may.
func (d *Deployer) Poll(ctx context.Context) error {
	branch, err := d.Git.Branch(ctx)
	if err != nil {
		return err
	}
	if branch != d.Cfg.Branch {
		return d.blocked(fmt.Sprintf("checkout is on %q, not %q", branch, d.Cfg.Branch))
	}
	dirty, err := d.Git.Dirty(ctx)
	if err != nil {
		return err
	}
	if dirty {
		return d.blocked("checkout has local changes")
	}
	remote, err := d.Git.Fetch(ctx, d.Cfg.Remote, d.Cfg.Branch)
	if err != nil {
		d.Log.Printf("fetch: %v", err)
		return err
	}
	head, err := d.Git.Head(ctx)
	if err != nil {
		return err
	}
	if remote == head || d.st.IsSkipped(remote) {
		return nil
	}
	ok, err := d.Git.IsAncestor(ctx, head, remote)
	if err != nil {
		return err
	}
	if !ok {
		return d.blocked("checkout has diverged from the remote branch")
	}

	if d.Cfg.CI != "off" {
		switch d.CI.Status(ctx, d.Cfg.Repo, remote) {
		case ci.Pending:
			d.Log.Printf("CI still running for %s", short(remote))
			return nil
		case ci.Failure:
			d.st.Skipped = append(d.st.Skipped, remote)
			d.st.LastResult = "ci-failed"
			d.event("ci-failed", remote, "", "CI failed; not deploying")
			return d.save()
		}
	}

	old, err := d.Compose.Config(ctx)
	if err != nil {
		return err
	}
	if err := d.Backup(ctx, old, remote); err != nil {
		d.st.LastResult = "backup-failed"
		d.event("backup-failed", remote, "", err.Error())
		if serr := d.save(); serr != nil {
			return serr
		}
		return err
	}

	d.st.Deploying = &state.Deploying{From: head, To: remote}
	if err := d.save(); err != nil {
		return err
	}
	if err := d.Git.FastForward(ctx, remote); err != nil {
		d.st.Deploying = nil
		_ = d.save()
		return err
	}
	return d.apply(ctx, head, remote, old)
}

// apply deploys the checkout (now at to) over from, whose config was old.
func (d *Deployer) apply(ctx context.Context, from, to string, old compose.Project) error {
	heldBefore := maps.Clone(d.st.Held)
	rollback := func(why string) error { return d.sharedRollback(ctx, from, to, heldBefore, why) }

	if err := d.Git.SubmoduleUpdate(ctx); err != nil {
		return rollback("submodules: " + err.Error())
	}
	cur, err := d.Compose.Config(ctx)
	if err != nil {
		return rollback("compose config: " + err.Error())
	}
	files, err := d.Git.ChangedFiles(ctx, from, to)
	if err != nil {
		return rollback(err.Error())
	}
	p := plan.Make(old, cur, files)
	for _, f := range files {
		if strings.HasPrefix(f, "deploy/") {
			d.event("deployer-outdated", to, "", "the deployer changed; run make autodeploy to update it")
			break
		}
	}

	// A held service is tried again when the commit changes (or removes) it.
	for _, c := range p.Changes {
		delete(d.st.Held, c.Service)
	}
	for name := range d.st.Held {
		if _, ok := cur.Services[name]; !ok {
			delete(d.st.Held, name)
		}
	}
	if err := d.writeOverride(cur); err != nil {
		return rollback(err.Error())
	}

	if p.Shared {
		if err := d.Compose.UpAllWait(ctx); err != nil {
			return rollback(strings.Join(p.SharedReasons, ", ") + ": " + err.Error())
		}
		return d.finish(ctx, to, nil)
	}

	heldNow := map[string]bool{}
	for _, c := range p.Changes {
		name := c.Service
		if c.Kind == plan.KindRemoved {
			if err := d.Compose.Remove(ctx, name); err != nil {
				return rollback("remove " + name + ": " + err.Error())
			}
			continue
		}
		s := cur.Services[name]
		if heldDependency(s, heldNow) {
			if err := d.hold(ctx, name, to, "dependency held", old, cur); err != nil {
				return rollback(err.Error())
			}
			heldNow[name] = true
			continue
		}
		if c.OneShot {
			if err := d.Compose.RunOnce(ctx, name); err != nil {
				return rollback(name + ": " + err.Error())
			}
			continue
		}
		if c.MountsOnly {
			if err := d.applyMounts(ctx, s); err != nil {
				return rollback(name + ": " + err.Error())
			}
			continue
		}

		img, err := d.Compose.ImageID(ctx, name)
		if err != nil {
			return rollback(err.Error())
		}
		derr := d.deployService(ctx, s)
		if derr == nil {
			continue
		}
		d.Log.Printf("%s unhealthy: %v", name, derr)
		heldNow[name] = true
		if c.Kind == plan.KindNew || img == "" {
			if err := d.Compose.Remove(ctx, name); err != nil {
				return rollback(err.Error())
			}
			d.st.Held[name] = state.Held{Service: name, Commit: to, Reason: "not deployed: " + derr.Error(), Since: d.Now()}
			d.event("held", to, name, "new service failed; not deployed")
			if err := d.writeOverride(cur); err != nil {
				return rollback(err.Error())
			}
			continue
		}
		d.st.Held[name] = state.Held{Service: name, Commit: to, Reason: derr.Error(), Since: d.Now(),
			Image: img, Definition: old.Services[name].Raw}
		d.event("held", to, name, derr.Error())
		if err := d.writeOverride(cur); err != nil {
			return rollback(err.Error())
		}
		if err := d.Compose.UpWait(ctx, name, false); err != nil {
			return rollback(name + "'s previous version is unhealthy too: " + err.Error())
		}
	}
	return d.finish(ctx, to, heldNow)
}

// hold keeps a service that wasn't deployed on what it runs now.
func (d *Deployer) hold(ctx context.Context, name, commit, reason string, old, cur compose.Project) error {
	img, err := d.Compose.ImageID(ctx, name)
	if err != nil {
		return err
	}
	h := state.Held{Service: name, Commit: commit, Reason: reason, Since: d.Now()}
	if prev, ok := old.Services[name]; ok && img != "" {
		h.Image, h.Definition = img, prev.Raw
	}
	d.st.Held[name] = h
	d.event("held", commit, name, reason)
	return d.writeOverride(cur)
}

func heldDependency(s compose.Service, held map[string]bool) bool {
	for _, dep := range s.DependsOn {
		if held[dep] {
			return true
		}
	}
	return false
}

func (d *Deployer) deployService(ctx context.Context, s compose.Service) error {
	if s.BuildContext != "" {
		if err := d.Compose.Build(ctx, s.Name); err != nil {
			return err
		}
	} else if err := d.Compose.Pull(ctx, s.Name); err != nil {
		return err
	}
	return d.Compose.UpWait(ctx, s.Name, false)
}

// applyMounts applies a change to a service's mounted files: check and
// reload in place when the labels say how, else recreate it.
func (d *Deployer) applyMounts(ctx context.Context, s compose.Service) error {
	if check := s.Labels[LabelCheck]; check != "" {
		if _, err := d.Compose.Exec(ctx, s.Name, nil, "sh", "-c", check); err != nil {
			return fmt.Errorf("check %q failed: %w", check, err)
		}
	}
	if reload := s.Labels[LabelReload]; reload != "" {
		_, err := d.Compose.Exec(ctx, s.Name, nil, "sh", "-c", reload)
		return err
	}
	return d.Compose.UpWait(ctx, s.Name, true)
}

// sharedRollback puts the checkout back on from (with the services held
// before), brings everything up, and never tries to again.
func (d *Deployer) sharedRollback(ctx context.Context, from, to string, held map[string]state.Held, why string) error {
	d.Log.Printf("rolling back %s: %s", short(to), why)
	var errs []error
	if err := d.Git.ResetKeep(ctx, from); err != nil {
		errs = append(errs, err)
	}
	if err := d.Git.SubmoduleUpdate(ctx); err != nil {
		errs = append(errs, err)
	}
	d.st.Held = maps.Clone(held)
	if d.st.Held == nil {
		d.st.Held = map[string]state.Held{}
	}
	if cur, err := d.Compose.Config(ctx); err == nil {
		if err := d.writeOverride(cur); err != nil {
			errs = append(errs, err)
		}
	} else {
		errs = append(errs, err)
	}
	if err := d.Compose.UpAllWait(ctx); err != nil {
		errs = append(errs, fmt.Errorf("previous version unhealthy too: %w", err))
	}
	d.st.Skipped = append(d.st.Skipped, to)
	d.st.Deploying = nil
	d.st.LastResult = "rolled-back"
	msg := why
	if err := errors.Join(errs...); err != nil {
		msg += "; rollback problems: " + err.Error()
	}
	d.event("rolled-back", to, "", msg)
	d.readExpiry(ctx)
	return d.save()
}

func (d *Deployer) finish(ctx context.Context, to string, held map[string]bool) error {
	subject, _ := d.Git.Subject(ctx, to)
	d.st.Deployed.Commit, d.st.Deployed.Subject, d.st.Deployed.At = to, subject, d.Now()
	d.st.Deploying = nil
	if len(held) > 0 {
		var names []string
		for n := range held {
			names = append(names, n)
		}
		sort.Strings(names)
		d.st.LastResult = "partial"
		d.event("partial", to, "", "deployed; held back: "+strings.Join(names, ", "))
	} else {
		d.st.LastResult = "deployed"
		d.event("deployed", to, "", subject)
	}
	d.readExpiry(ctx)
	return d.save()
}

func (d *Deployer) writeOverride(cur compose.Project) error {
	defs := map[string]json.RawMessage{}
	for n, s := range cur.Services {
		defs[n] = s.Raw
	}
	return state.WriteOverride(d.Cfg.StateDir, d.st.Held, defs)
}

// readExpiry reads the certificate expiry from services labelled
// homeserver.tls (the earliest wins).
func (d *Deployer) readExpiry(ctx context.Context) {
	p, err := d.Compose.Config(ctx)
	if err != nil {
		return
	}
	var earliest time.Time
	for _, name := range p.Order() {
		port := p.Services[name].Labels[LabelTLS]
		if port == "" {
			continue
		}
		t, err := d.Expiry(ctx, name, port)
		if err != nil {
			d.Log.Printf("certificate of %s: %v", name, err)
			continue
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	if !earliest.IsZero() {
		d.st.CertExpiry = earliest
	}
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
