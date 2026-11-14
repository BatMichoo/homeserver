package deployer

import (
	"context"
	"time"
)

// Schedule runs one-shot services labelled homeserver.every whose time
// has come (today: the certificate check), then reloads the services that
// depend on them and have a reload command (nginx, for a renewed
// certificate). A failure is recorded and retried at the next tick.
// Nothing runs while the checkout is on another branch or modified: it
// would run code that never passed CI.
func (d *Deployer) Schedule(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
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
		for _, other := range p.Order() {
			o := p.Services[other]
			reload := o.Labels[LabelReload]
			if reload == "" || !dependsOn(o.DependsOn, name) {
				continue
			}
			if _, err := d.Compose.Exec(ctx, other, nil, "sh", "-c", reload); err != nil {
				d.event("scheduled-failed", "", other, "reload after "+name+": "+err.Error())
			}
		}
		d.event("scheduled-ok", "", name, "ran on schedule")
	}
	if !ran {
		return nil
	}
	d.readExpiry(ctx)
	return d.save()
}

func dependsOn(deps []string, name string) bool {
	for _, d := range deps {
		if d == name {
			return true
		}
	}
	return false
}
