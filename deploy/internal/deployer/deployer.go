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

// heldTag prefixes the tags that keep held services' images from being
// pruned.
const heldTag = "homeserver-held/"

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
	RemoveStray(ctx context.Context, paths []string) error
	Subject(ctx context.Context, sha string) (string, error)
}

// Compose runs the project.
type Compose interface {
	Config(ctx context.Context) (compose.Project, error)
	Build(ctx context.Context, svc string) error
	Pull(ctx context.Context, svc string) error
	PrepareAll(ctx context.Context) error
	UpWait(ctx context.Context, svc string, recreate bool) error
	UpAllWait(ctx context.Context) error
	RunOnce(ctx context.Context, svc string) error
	Exec(ctx context.Context, svc string, stdin io.Reader, cmd ...string) ([]byte, error)
	ImageID(ctx context.Context, svc string) (string, error)
	Running(ctx context.Context, svc string) (bool, error)
	Remove(ctx context.Context, svc string) error
	Tag(ctx context.Context, image, ref string) error
	Untag(ctx context.Context, ref string) error
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

// safe reports why the checkout can't be used, or "" when it can.
func (d *Deployer) safe(ctx context.Context) (string, error) {
	branch, err := d.Git.Branch(ctx)
	if err != nil {
		return "", err
	}
	if branch != d.Cfg.Branch {
		return fmt.Sprintf("checkout is on %q, not %q", branch, d.Cfg.Branch), nil
	}
	dirty, err := d.Git.Dirty(ctx)
	if err != nil {
		return "", err
	}
	if dirty {
		return "checkout has local changes", nil
	}
	return "", nil
}

// Resume finishes a deploy that was cut short: if the checkout already
// moved to the new commit, it's rolled back as a failed shared deploy.
func (d *Deployer) Resume(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
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
	files, _ := d.Git.ChangedFiles(ctx, dep.From, dep.To)
	d.event("resumed", dep.To, "", "a deploy was interrupted; rolling it back")
	return d.rollback(ctx, dep.From, dep.To, d.st.Held, files, "the deploy was interrupted", true)
}

// Poll deploys the remote branch's newest commit, if it may.
func (d *Deployer) Poll(ctx context.Context) error {
	if why, err := d.safe(ctx); err != nil || why != "" {
		if err != nil {
			return err
		}
		return d.blocked(why)
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
			d.Log.Printf("CI still running (or GitHub unreachable) for %s", short(remote))
			return nil
		case ci.Failure:
			d.st.Skipped = append(d.st.Skipped, remote)
			d.st.LastResult = "ci-failed"
			d.event("ci-failed", remote, "", "CI failed; not deploying")
			return d.save()
		}
	}

	// From here on a stop signal waits for the deploy to finish, so it's
	// never left halfway (the container's stop grace period allows for it).
	ctx = context.WithoutCancel(ctx)

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
	files, err := d.Git.ChangedFiles(ctx, head, remote)
	if err != nil {
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
	return d.apply(ctx, head, remote, old, files)
}

// apply deploys the checkout (now at to) over from, whose config was old.
func (d *Deployer) apply(ctx context.Context, from, to string, old compose.Project, files []string) error {
	heldBefore := maps.Clone(d.st.Held)
	shared := func(why string) error { return d.rollback(ctx, from, to, heldBefore, files, why, true) }
	retry := func(why string, restore bool) error {
		return d.retryLater(ctx, from, to, heldBefore, files, why, restore)
	}

	if err := d.Git.SubmoduleUpdate(ctx); err != nil {
		return shared("submodules: " + err.Error())
	}
	if err := d.Git.RemoveStray(ctx, files); err != nil {
		return shared("cleaning the checkout: " + err.Error())
	}
	cur, err := d.Compose.Config(ctx)
	if err != nil {
		return shared("compose config: " + err.Error())
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
		return shared(err.Error())
	}

	if p.Shared {
		if err := d.Compose.PrepareAll(ctx); err != nil {
			return retry("building or pulling images: "+err.Error(), false)
		}
		if err := d.Compose.UpAllWait(ctx); err != nil {
			return shared(strings.Join(p.SharedReasons, ", ") + ": " + err.Error())
		}
		return d.finish(ctx, to, heldBefore, nil)
	}

	blocked := map[string]bool{} // held or not run in this deploy; dependents follow
	for _, c := range p.Changes {
		name := c.Service
		if c.Kind == plan.KindRemoved {
			if err := d.Compose.Remove(ctx, name); err != nil {
				return shared("remove " + name + ": " + err.Error())
			}
			continue
		}
		s := cur.Services[name]
		prev, wasHeld := heldBefore[name]
		if wasHeld && prev.Definition == nil {
			wasHeld = false // never deployed: treat like a new service
		}

		if dependsOnAny(s, blocked) {
			blocked[name] = true
			if c.OneShot {
				d.event("held", to, name, "not run: a service it depends on is held")
				continue
			}
			if err := d.holdUndeployed(ctx, cur, old, name, to, wasHeld, prev, "dependency held"); err != nil {
				return shared(err.Error())
			}
			continue
		}
		if c.OneShot {
			if err := d.Compose.RunOnce(ctx, name); err != nil {
				return shared(name + ": " + err.Error())
			}
			continue
		}
		if c.MountsOnly && !wasHeld {
			if err := d.applyMounts(ctx, s); err != nil {
				return shared(name + ": " + err.Error())
			}
			continue
		}

		img, err := d.Compose.ImageID(ctx, name)
		if err != nil {
			return shared(err.Error())
		}
		if err := d.prepare(ctx, s); err != nil {
			return retry(name+": building or pulling: "+err.Error(), true)
		}
		uerr := d.Compose.UpWait(ctx, name, false)
		if uerr == nil {
			continue
		}
		d.Log.Printf("%s unhealthy: %v", name, uerr)
		blocked[name] = true

		h, heldOnce := heldBefore[name]
		neverDeployed := c.Kind == plan.KindNew || (heldOnce && h.Definition == nil)
		switch {
		case neverDeployed:
			// A new service that fails is left out, unless others need it.
			if hasDependents(cur, name) {
				return shared(name + " (new) is unhealthy and other services depend on it")
			}
			if err := d.Compose.Remove(ctx, name); err != nil {
				return shared(err.Error())
			}
			d.st.Held[name] = state.Held{Service: name, Commit: to, Reason: "not deployed: " + short500(uerr.Error()), Since: d.Now()}
			d.event("held", to, name, "new service is unhealthy; not deployed")
			if err := d.writeOverride(cur); err != nil {
				return shared(err.Error())
			}
		case img == "":
			// No previous container to go back to.
			return shared(name + " is unhealthy and had no previous container to go back to")
		default:
			def := old.Services[name].Raw
			if wasHeld {
				def = prev.Definition // what it was held on, not the version that failed before
			}
			if err := d.pin(ctx, cur, name, to, img, def, short500(uerr.Error())); err != nil {
				return shared(err.Error())
			}
			if err := d.Compose.UpWait(ctx, name, false); err != nil {
				return shared(name + "'s previous version is unhealthy too: " + err.Error())
			}
		}
	}
	return d.finish(ctx, to, heldBefore, blocked)
}

// holdUndeployed keeps a service whose dependency is held on what it runs
// now (it isn't deployed).
func (d *Deployer) holdUndeployed(ctx context.Context, cur, old compose.Project, name, to string, wasHeld bool, prev state.Held, reason string) error {
	if wasHeld {
		d.st.Held[name] = prev
		d.event("held", to, name, reason)
		return d.writeOverride(cur)
	}
	prevDef, existed := old.Services[name]
	if !existed {
		if hasDependents(cur, name) {
			return fmt.Errorf("%s (new) can't be deployed and other services depend on it", name)
		}
		d.st.Held[name] = state.Held{Service: name, Commit: to, Reason: "not deployed: " + reason, Since: d.Now()}
		d.event("held", to, name, reason)
		return d.writeOverride(cur)
	}
	img, err := d.Compose.ImageID(ctx, name)
	if err != nil {
		return err
	}
	if img == "" {
		return fmt.Errorf("%s: a dependency is held and it has no container to keep", name)
	}
	return d.pin(ctx, cur, name, to, img, prevDef.Raw, reason)
}

// pin holds a service on an image and definition. The image gets a tag,
// so pruning can't delete it while it's held.
func (d *Deployer) pin(ctx context.Context, cur compose.Project, name, commit, img string, def json.RawMessage, reason string) error {
	ref := img
	if !strings.HasPrefix(img, heldTag) {
		ref = heldTag + name + ":" + short(commit)
		if err := d.Compose.Tag(ctx, img, ref); err != nil {
			return fmt.Errorf("tag %s: %w", name, err)
		}
	}
	d.st.Held[name] = state.Held{Service: name, Commit: commit, Reason: reason, Since: d.Now(), Image: ref, Definition: def}
	d.event("held", commit, name, reason)
	return d.writeOverride(cur)
}

func dependsOnAny(s compose.Service, set map[string]bool) bool {
	for _, dep := range s.DependsOn {
		if set[dep] {
			return true
		}
	}
	return false
}

func hasDependents(p compose.Project, name string) bool {
	for _, s := range p.Services {
		for _, dep := range s.DependsOn {
			if dep == name {
				return true
			}
		}
	}
	return false
}

// prepare builds or pulls a service's new image.
func (d *Deployer) prepare(ctx context.Context, s compose.Service) error {
	if s.BuildContext != "" {
		return d.Compose.Build(ctx, s.Name)
	}
	return d.Compose.Pull(ctx, s.Name)
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

// rollback puts the checkout back on from (with the services held before)
// and brings everything up. A rolled-back commit is skipped for good
// (skip); a commit that only failed to build or pull isn't, and is tried
// again next poll.
func (d *Deployer) rollback(ctx context.Context, from, to string, heldBefore map[string]state.Held, files []string, why string, skip bool) error {
	return d.undo(ctx, from, to, heldBefore, files, why, skip, true, "rolled-back")
}

func (d *Deployer) retryLater(ctx context.Context, from, to string, heldBefore map[string]state.Held, files []string, why string, restore bool) error {
	return d.undo(ctx, from, to, heldBefore, files, why, false, restore, "build-failed")
}

func (d *Deployer) undo(ctx context.Context, from, to string, heldBefore map[string]state.Held, files []string, why string, skip, restore bool, result string) error {
	d.Log.Printf("undoing %s: %s", short(to), why)
	d.releaseTags(ctx, d.st.Held, heldBefore) // tags made in this deploy

	// If the checkout can't be put back, don't bring anything up on the
	// bad commit: record it and let a person look (Resume retries on the
	// next start, since Deploying stays set).
	if err := d.Git.ResetKeep(ctx, from); err != nil {
		return d.rollbackFailed(to, why, err)
	}
	if err := d.Git.SubmoduleUpdate(ctx); err != nil {
		return d.rollbackFailed(to, why, err)
	}
	if err := d.Git.RemoveStray(ctx, files); err != nil {
		return d.rollbackFailed(to, why, err)
	}

	var problems []error
	d.st.Held = maps.Clone(heldBefore)
	if d.st.Held == nil {
		d.st.Held = map[string]state.Held{}
	}
	if cur, err := d.Compose.Config(ctx); err == nil {
		if err := d.writeOverride(cur); err != nil {
			problems = append(problems, err)
		}
	} else {
		problems = append(problems, err)
	}
	if restore {
		if err := d.Compose.UpAllWait(ctx); err != nil {
			problems = append(problems, fmt.Errorf("previous version unhealthy too: %w", err))
		}
	}
	if skip {
		d.st.Skipped = append(d.st.Skipped, to)
	}
	d.st.Deploying = nil
	d.st.LastResult = result
	msg := why
	if err := errors.Join(problems...); err != nil {
		msg += "; problems while undoing: " + err.Error()
	}
	if !skip {
		msg += " (will try again)"
	}
	d.event(result, to, "", short500(msg))
	d.readExpiry(ctx)
	return d.save()
}

func (d *Deployer) rollbackFailed(to, why string, err error) error {
	d.st.LastResult = "rollback-failed"
	d.event("rollback-failed", to, "", short500(why+"; could not put the checkout back: "+err.Error()+"; fix the checkout by hand"))
	return d.save()
}

func (d *Deployer) finish(ctx context.Context, to string, heldBefore map[string]state.Held, blocked map[string]bool) error {
	d.releaseTags(ctx, heldBefore, d.st.Held) // holds this deploy released
	subject, _ := d.Git.Subject(ctx, to)
	d.st.Deployed.Commit, d.st.Deployed.Subject, d.st.Deployed.At = to, subject, d.Now()
	d.st.Deploying = nil
	if len(blocked) > 0 {
		var names []string
		for n := range blocked {
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

// releaseTags removes the held-image tags in from that keep isn't using.
func (d *Deployer) releaseTags(ctx context.Context, from, keep map[string]state.Held) {
	inUse := map[string]bool{}
	for _, h := range keep {
		inUse[h.Image] = true
	}
	for _, h := range from {
		if strings.HasPrefix(h.Image, heldTag) && !inUse[h.Image] {
			if err := d.Compose.Untag(ctx, h.Image); err != nil {
				d.Log.Printf("untag %s: %v", h.Image, err)
			}
		}
	}
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

// short500 keeps messages (often whole command output) readable in the
// status and on the dashboard.
func short500(s string) string {
	if len(s) > 500 {
		return s[:500] + "…"
	}
	return s
}
