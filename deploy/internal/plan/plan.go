// Package plan works out what a commit changed, from the compose configs
// before and after it and the files it touched.
package plan

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/BatMichoo/homeserver/deploy/internal/compose"
)

// Kind of change to a service.
type Kind string

const (
	KindChanged Kind = "changed"
	KindNew     Kind = "new"
	KindRemoved Kind = "removed"
)

// Reasons a service counts as changed.
const (
	ReasonDefinition = "definition"
	ReasonBuild      = "build context"
	ReasonMounts     = "mounted files"
)

// Change is one service to deploy (or remove).
type Change struct {
	Service    string
	Kind       Kind
	Reasons    []string
	MountsOnly bool // only bind-mounted files changed (can be reloaded in place)
	OneShot    bool // restart: "no" (run, not kept running)
}

// Plan is what a commit asks for.
type Plan struct {
	Changes       []Change // dependency order of the new config; removed services last
	Shared        bool     // can't be undone one service at a time
	SharedReasons []string
}

// Empty is true when no service changed.
func (p Plan) Empty() bool { return len(p.Changes) == 0 && !p.Shared }

// Make compares the old and new configs (bind sources and build contexts
// relative to the project) and the files the commit changed.
func Make(old, new compose.Project, changedFiles []string) Plan {
	var p Plan
	if old.Name != new.Name {
		p.shared("project name changed")
	}
	if !sameJSON(old.Networks, new.Networks) {
		p.shared("networks changed")
	}
	if !sameJSON(old.Volumes, new.Volumes) {
		p.shared("volumes changed")
	}

	for _, name := range new.Order() {
		s := new.Services[name]
		prev, existed := old.Services[name]
		var c Change
		if !existed {
			c = Change{Service: name, Kind: KindNew}
		} else {
			reasons := changeReasons(prev, s, changedFiles)
			if len(reasons) == 0 {
				continue
			}
			c = Change{Service: name, Kind: KindChanged, Reasons: reasons,
				MountsOnly: len(reasons) == 1 && reasons[0] == ReasonMounts}
		}
		c.OneShot = !s.LongRunning()
		if s.LongRunning() && !s.Healthcheck {
			p.shared(name + " has no healthcheck")
		}
		p.Changes = append(p.Changes, c)
	}
	for _, name := range old.Order() {
		if _, ok := new.Services[name]; !ok {
			p.Changes = append(p.Changes, Change{Service: name, Kind: KindRemoved, OneShot: !old.Services[name].LongRunning()})
		}
	}
	return p
}

func (p *Plan) shared(reason string) {
	p.Shared = true
	p.SharedReasons = append(p.SharedReasons, reason)
}

func changeReasons(old, new compose.Service, files []string) []string {
	var reasons []string
	if !sameJSON(old.Raw, new.Raw) {
		reasons = append(reasons, ReasonDefinition)
	}
	paths := new.BuildPaths
	if len(paths) == 0 && new.BuildContext != "" {
		paths = []string{new.BuildContext}
	}
	for _, p := range paths {
		if anyUnder(files, p) {
			reasons = append(reasons, ReasonBuild)
			break
		}
	}
	for _, m := range new.Mounts {
		if m.Type == "bind" && anyUnder(files, m.Source) {
			reasons = append(reasons, ReasonMounts)
			break
		}
	}
	return reasons
}

// anyUnder is true when a changed path is path itself, inside it, or
// contains it. The last case is a submodule bump: git lists the
// submodule's path (apps/expenser) for a build context inside it
// (apps/expenser/server). Git lists files, never plain directories, so
// only submodules can contain another path.
func anyUnder(files []string, path string) bool {
	path = strings.TrimSuffix(strings.TrimPrefix(path, "./"), "/")
	if path == "." {
		return len(files) > 0 // the whole repository
	}
	if path == "" || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "..") {
		return false // outside the repository: git can't see it change
	}
	for _, f := range files {
		if f == path || strings.HasPrefix(f, path+"/") || strings.HasPrefix(path, f+"/") {
			return true
		}
	}
	return false
}

// sameJSON compares JSON values regardless of spacing and key order.
func sameJSON(a, b json.RawMessage) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	na, _ := json.Marshal(va)
	nb, _ := json.Marshal(vb)
	return bytes.Equal(na, nb)
}
