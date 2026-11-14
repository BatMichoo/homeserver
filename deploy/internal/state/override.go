package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WriteOverride writes dir/override.yaml, a compose file that keeps held
// services on their previous version, or removes it when nothing needs
// pinning. newDefs are the services' definitions in the checkout's
// compose file, so keys the new version added can be reset.
//
// For a held service: each key of its old definition with !override (so
// it replaces, not merges), its image pinned with no pull and no build,
// and keys only the new definition has reset. A service that was never
// deployed is put in a "held" profile, so compose doesn't start it.
func WriteOverride(dir string, held map[string]Held, newDefs map[string]json.RawMessage) error {
	path := filepath.Join(dir, "override.yaml")
	var names []string
	for n := range held {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}

	var b strings.Builder
	b.WriteString("# Written by the deployer: services held on their previous version.\nservices:\n")
	for _, n := range names {
		h := held[n]
		fmt.Fprintf(&b, "  %s:\n", n)
		if h.Definition == nil {
			b.WriteString("    profiles: !override [\"held\"]\n")
			continue
		}
		lines := map[string]string{}
		var old map[string]json.RawMessage
		if err := json.Unmarshal(h.Definition, &old); err != nil {
			return fmt.Errorf("held %s: %w", n, err)
		}
		for k, v := range old {
			switch k {
			case "build", "image", "pull_policy":
				continue
			}
			lines[k] = "!override " + compact(v)
		}
		var cur map[string]json.RawMessage
		if d, ok := newDefs[n]; ok {
			if err := json.Unmarshal(d, &cur); err != nil {
				return fmt.Errorf("service %s: %w", n, err)
			}
		}
		for k := range cur {
			if _, ok := lines[k]; !ok {
				lines[k] = "!reset null"
			}
		}
		image, _ := json.Marshal(h.Image)
		lines["image"] = "!override " + string(image)
		lines["pull_policy"] = `!override "never"`
		lines["build"] = "!reset null"

		var keys []string
		for k := range lines {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "    %s: %s\n", k, lines[k])
		}
	}
	return writeAtomic(path, []byte(b.String()))
}

// compact is v as one-line JSON (JSON is valid YAML flow syntax).
func compact(v json.RawMessage) string {
	var x any
	if json.Unmarshal(v, &x) != nil {
		return string(v)
	}
	b, _ := json.Marshal(x)
	return string(b)
}
