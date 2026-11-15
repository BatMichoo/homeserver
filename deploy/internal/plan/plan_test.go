package plan

import (
	"encoding/json"
	"testing"

	"github.com/BatMichoo/homeserver/deploy/internal/compose"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// svc builds a service; raw is its definition as compose would print it.
func svc(name string, opts ...func(*compose.Service)) compose.Service {
	s := compose.Service{Name: name, Restart: "unless-stopped", Healthcheck: true, Labels: map[string]string{}}
	for _, o := range opts {
		o(&s)
	}
	if s.Raw == nil {
		s.Raw = json.RawMessage(`{"image":"` + s.Image + `"}`)
	}
	return s
}

func build(ctx string) func(*compose.Service) {
	return func(s *compose.Service) { s.BuildContext = ctx }
}
func mount(src string) func(*compose.Service) {
	return func(s *compose.Service) { s.Mounts = append(s.Mounts, compose.Mount{Type: "bind", Source: src}) }
}
func raw(r string) func(*compose.Service) {
	return func(s *compose.Service) { s.Raw = json.RawMessage(r) }
}
func deps(d ...string) func(*compose.Service) {
	return func(s *compose.Service) { s.DependsOn = d }
}
func oneShot(s *compose.Service)  { s.Restart = "no"; s.Healthcheck = false }
func noHealth(s *compose.Service) { s.Healthcheck = false }

func project(services ...compose.Service) compose.Project {
	p := compose.Project{Name: "homeserver", Services: map[string]compose.Service{},
		Networks: json.RawMessage(`{"default":{}}`), Volumes: json.RawMessage(`{"db-data":{}}`)}
	for _, s := range services {
		p.Services[s.Name] = s
	}
	return p
}

func base() compose.Project {
	return project(
		svc("db", raw(`{"image":"postgres:16"}`)),
		svc("auth", build("auth"), deps("db"), raw(`{"build":{"context":"auth"}}`)),
		svc("nginx", mount("nginx/sites"), mount("nginx/nginx.conf"), deps("auth", "certs"), raw(`{"image":"nginx"}`)),
		svc("certs", build("certs"), oneShot, raw(`{"build":{"context":"certs"},"restart":"no"}`)),
		svc("expenser", build("apps/expenser/server"), raw(`{"build":{"context":"apps/expenser/server"}}`)),
	)
}

func names(p Plan) []string {
	var out []string
	for _, c := range p.Changes {
		out = append(out, c.Service)
	}
	return out
}

func change(t *testing.T, p Plan, name string) Change {
	t.Helper()
	for _, c := range p.Changes {
		if c.Service == name {
			return c
		}
	}
	require.Failf(t, "no change", "for %s in %v", name, names(p))
	return Change{}
}

func TestPlanNoChanges(t *testing.T) {
	p := Make(base(), base(), []string{"README.md", "docs/x.md"})
	assert.True(t, p.Empty())
	assert.False(t, p.Shared)
}

func TestPlanSubmoduleBump(t *testing.T) {
	p := Make(base(), base(), []string{"apps/expenser"})
	assert.Equal(t, []string{"expenser"}, names(p))
	c := change(t, p, "expenser")
	assert.Equal(t, KindChanged, c.Kind)
	assert.Equal(t, []string{"build context"}, c.Reasons)
}

func TestPlanBuildFile(t *testing.T) {
	p := Make(base(), base(), []string{"auth/internal/x.go"})
	assert.Equal(t, []string{"auth"}, names(p))
}

func TestPlanMountedFile(t *testing.T) {
	p := Make(base(), base(), []string{"nginx/sites/a.conf"})
	c := change(t, p, "nginx")
	assert.True(t, c.MountsOnly)
	assert.Equal(t, []string{"mounted files"}, c.Reasons)
	p = Make(base(), base(), []string{"nginx/nginx.conf"})
	assert.True(t, change(t, p, "nginx").MountsOnly, "a mounted file itself")
}

func TestPlanPrefixNotPath(t *testing.T) {
	p := Make(base(), base(), []string{"nginx/sites2/x", "authority/x", "apps/expenser-old/x"})
	assert.True(t, p.Empty())
}

func TestPlanDefinition(t *testing.T) {
	nw := base()
	s := nw.Services["db"]
	s.Raw = json.RawMessage(`{"image":"postgres:16","environment":{"X":"1"}}`)
	nw.Services["db"] = s
	p := Make(base(), nw, nil)
	c := change(t, p, "db")
	assert.Equal(t, []string{"definition"}, c.Reasons)
	assert.False(t, c.MountsOnly)
}

func TestPlanDefinitionSameJSONDifferentSpacing(t *testing.T) {
	nw := base()
	s := nw.Services["db"]
	s.Raw = json.RawMessage(`{ "image" : "postgres:16" }`)
	nw.Services["db"] = s
	assert.True(t, Make(base(), nw, nil).Empty())
}

func TestPlanImagePin(t *testing.T) {
	nw := base()
	s := nw.Services["db"]
	s.Raw = json.RawMessage(`{"image":"postgres:16@sha256:abc"}`)
	nw.Services["db"] = s
	assert.Equal(t, []string{"definition"}, change(t, Make(base(), nw, nil), "db").Reasons)
}

func TestPlanMountsAndDefinition(t *testing.T) {
	nw := base()
	s := nw.Services["nginx"]
	s.Raw = json.RawMessage(`{"image":"nginx:1.27"}`)
	nw.Services["nginx"] = s
	c := change(t, Make(base(), nw, []string{"nginx/sites/a.conf"}), "nginx")
	assert.Equal(t, []string{"definition", "mounted files"}, c.Reasons)
	assert.False(t, c.MountsOnly)
}

func TestPlanNewAndRemoved(t *testing.T) {
	nw := base()
	delete(nw.Services, "expenser")
	nw.Services["media"] = svc("media", raw(`{"image":"media"}`))
	p := Make(base(), nw, nil)
	assert.Equal(t, KindNew, change(t, p, "media").Kind)
	assert.Equal(t, KindRemoved, change(t, p, "expenser").Kind)
	assert.Equal(t, "expenser", p.Changes[len(p.Changes)-1].Service, "removed last")
}

func TestPlanOrder(t *testing.T) {
	p := Make(base(), base(), []string{"nginx/sites/a.conf", "auth/x.go", "certs/x.sh"})
	assert.Equal(t, []string{"auth", "certs", "nginx"}, names(p))
}

func TestPlanOneShot(t *testing.T) {
	p := Make(base(), base(), []string{"certs/issue-certs.sh"})
	c := change(t, p, "certs")
	assert.True(t, c.OneShot)
	assert.False(t, p.Shared)
}

func TestPlanSharedNetworks(t *testing.T) {
	nw := base()
	nw.Networks = json.RawMessage(`{"default":{},"other":{}}`)
	p := Make(base(), nw, nil)
	assert.True(t, p.Shared)
	assert.Contains(t, p.SharedReasons, "networks changed")

	nw = base()
	nw.Volumes = json.RawMessage(`{}`)
	assert.Contains(t, Make(base(), nw, nil).SharedReasons, "volumes changed")
	nw = base()
	nw.Name = "other"
	assert.Contains(t, Make(base(), nw, nil).SharedReasons, "project name changed")
}

func TestPlanSharedNoHealthcheck(t *testing.T) {
	nw := base()
	s := nw.Services["auth"]
	s.Healthcheck = false
	nw.Services["auth"] = s
	p := Make(base(), nw, []string{"auth/x.go"})
	assert.True(t, p.Shared)
	assert.Contains(t, p.SharedReasons, "auth has no healthcheck")

	nw = base()
	nw.Services["web"] = svc("web", noHealth, raw(`{"image":"web"}`))
	assert.True(t, Make(base(), nw, nil).Shared, "new service without healthcheck")
	assert.False(t, Make(base(), base(), []string{"README.md"}).Shared, "unchanged services aren't checked")
}

func TestPlanWholeRepoContext(t *testing.T) {
	old := project(svc("all", build("."), raw(`{"build":{"context":"."}}`)))
	assert.Equal(t, []string{"all"}, names(Make(old, old, []string{"README.md"})))
	assert.True(t, Make(old, old, nil).Empty())
}

func TestPlanDockerfileAndExtraContexts(t *testing.T) {
	old := project(svc("web", func(s *compose.Service) {
		s.BuildContext = "web"
		s.BuildPaths = []string{"web", "docker/web.Dockerfile", "shared"}
	}, raw(`{"build":{"context":"web"}}`)))
	assert.Equal(t, []string{"web"}, names(Make(old, old, []string{"docker/web.Dockerfile"})))
	assert.Equal(t, []string{"web"}, names(Make(old, old, []string{"shared/lib.go"})))
	assert.True(t, Make(old, old, []string{"docker/other"}).Empty())
}
