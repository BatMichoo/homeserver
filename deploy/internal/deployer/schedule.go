package deployer

import (
	"context"
	"time"

	"github.com/BatMichoo/homeserver/deploy/internal/compose"
)

// Schedule runs one-shot services labelled homeserver.every whose time
// has come (today: the certificate check), then reloads the services that
// depend on them and have a reload command (nginx, for a renewed
// certificate). A failure is recorded and retried at the next tick.
// Nothing runs while the checkout is on another branch or modified: it
// would run code that never passed CI.
func (d *Deployer) Schedule(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
	unlock, err := d.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if why, err := d.safe(ctx); err != nil || why != "" {
		return err
	}
	p, err := d.Compose.Config(ctx)
	if err != nil {
		return err
	}
	ran := false
	for _, name := range p.Order() {
		s := p.Services[name]
		every, err := time.ParseDuration(s.Labels[LabelEvery])
		if err != nil || every <= 0 || s.LongRunning() {
			continue
		}
		if last := d.st.LastRun[name]; !last.IsZero() && d.Now().Before(last.Add(every)) {
			continue
		}
		ran = true
		if err := d.Compose.RunOnce(ctx, name); err != nil {
			d.event("scheduled-failed", "", name, err.Error())
			continue
		}
		d.st.LastRun[name] = d.Now()
		d.reloadDependents(ctx, p, name)
		d.event("scheduled-ok", "", name, "ran on schedule")
	}
	if !ran {
		return nil
	}
	d.readExpiry(ctx)
	return d.save()
}

// reloadDependents runs the reload command of every service that depends
// on the one-shot name (nginx after certs renewed the certificate).
func (d *Deployer) reloadDependents(ctx context.Context, p compose.Project, name string) {
	for _, other := range p.Order() {
		o := p.Services[other]
		reload := o.Labels[LabelReload]
		if reload == "" || !dependsOn(o.DependsOn, name) {
			continue
		}
		if _, err := d.Compose.Exec(ctx, other, nil, "sh", "-c", reload); err != nil {
			d.event("reload-failed", "", other, "reload after "+name+": "+err.Error())
		}
	}
}

func dependsOn(deps []string, name string) bool {
	for _, d := range deps {
		if d == name {
			return true
		}
	}
	return false
}
