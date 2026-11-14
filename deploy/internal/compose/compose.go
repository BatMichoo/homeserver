// Package compose reads a Docker Compose project's resolved configuration
// (`docker compose config --format json`) into what the deployer needs.
package compose

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Mount is a volume of a service.
type Mount struct{ Type, Source, Target string }

// Service is one compose service.
type Service struct {
	Name         string
	Image        string
	BuildContext string // relative to the project; "" without build
	Restart      string
	DependsOn    []string
	Labels       map[string]string
	Mounts       []Mount
	Healthcheck  bool            // defined and not disabled
	Raw          json.RawMessage // as compose printed it, with project paths made relative
}

// LongRunning is true for services meant to keep running (not one-shots
// marked restart: "no").
func (s Service) LongRunning() bool { return s.Restart != "no" }

// Project is a resolved compose project.
type Project struct {
	Name     string
	Dir      string
	Services map[string]Service
	Networks json.RawMessage
	Volumes  json.RawMessage
}

type rawProject struct {
	Name     string                     `json:"name"`
	Services map[string]json.RawMessage `json:"services"`
	Networks json.RawMessage            `json:"networks"`
	Volumes  json.RawMessage            `json:"volumes"`
}

type rawService struct {
	Image string `json:"image"`
	Build *struct {
		Context string `json:"context"`
	} `json:"build"`
	Restart   string                     `json:"restart"`
	DependsOn map[string]json.RawMessage `json:"depends_on"`
	Labels    map[string]string          `json:"labels"`
	Volumes   []struct {
		Type   string `json:"type"`
		Source string `json:"source"`
		Target string `json:"target"`
	} `json:"volumes"`
	Healthcheck *struct {
		Disable bool     `json:"disable"`
		Test    []string `json:"test"`
	} `json:"healthcheck"`
}

// Parse reads `docker compose config --format json` output. Paths under
// projectDir become relative to it, so configs from different checkouts
// compare equal.
func Parse(data []byte, projectDir string) (Project, error) {
	dir := filepath.Clean(projectDir)
	// Make project paths relative everywhere, including in Raw.
	data = bytes.ReplaceAll(data, []byte(`"`+dir+`/`), []byte(`"`))
	data = bytes.ReplaceAll(data, []byte(`"`+dir+`"`), []byte(`"."`))

	var rp rawProject
	if err := json.Unmarshal(data, &rp); err != nil {
		return Project{}, fmt.Errorf("compose config: %w", err)
	}
	p := Project{Name: rp.Name, Dir: dir, Services: map[string]Service{}, Networks: rp.Networks, Volumes: rp.Volumes}
	for name, raw := range rp.Services {
		var rs rawService
		if err := json.Unmarshal(raw, &rs); err != nil {
			return Project{}, fmt.Errorf("compose config: service %s: %w", name, err)
		}
		s := Service{
			Name: name, Image: rs.Image, Restart: rs.Restart, Labels: rs.Labels, Raw: raw,
			Healthcheck: rs.Healthcheck != nil && !rs.Healthcheck.Disable &&
				!(len(rs.Healthcheck.Test) > 0 && rs.Healthcheck.Test[0] == "NONE"),
		}
		if s.Labels == nil {
			s.Labels = map[string]string{}
		}
		if rs.Build != nil {
			s.BuildContext = strings.TrimSuffix(filepath.ToSlash(rs.Build.Context), "/")
		}
		for d := range rs.DependsOn {
			s.DependsOn = append(s.DependsOn, d)
		}
		sort.Strings(s.DependsOn)
		for _, v := range rs.Volumes {
			s.Mounts = append(s.Mounts, Mount{Type: v.Type, Source: filepath.ToSlash(v.Source), Target: v.Target})
		}
		p.Services[name] = s
	}
	return p, nil
}

// Order lists the services so each comes after what it depends on (ties
// by name, for a stable order).
func (p Project) Order() []string {
	var names []string
	for n := range p.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	done := map[string]bool{}
	var out []string
	var visit func(string)
	visit = func(n string) {
		if done[n] {
			return
		}
		done[n] = true
		for _, d := range p.Services[n].DependsOn {
			if _, ok := p.Services[d]; ok {
				visit(d)
			}
		}
		out = append(out, n)
	}
	for _, n := range names {
		visit(n)
	}
	return out
}

// Lint reports long-running services without a healthcheck (the deployer
// relies on healthchecks to know a deploy worked). A service labelled
// homeserver.lint: skip is left out.
func Lint(p Project) []string {
	var problems []string
	for _, n := range p.Order() {
		s := p.Services[n]
		if s.LongRunning() && !s.Healthcheck && s.Labels["homeserver.lint"] != "skip" {
			problems = append(problems, fmt.Sprintf("service %s: long-running but has no healthcheck", n))
		}
	}
	sort.Strings(problems)
	return problems
}
