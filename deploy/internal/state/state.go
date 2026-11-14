// Package state is what the deployer remembers between polls (.deploy/
// in the checkout): skipped commits, held services, recent events, and
// the files it derives from them (override.yaml, public/status.json).
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const maxEvents = 50

// Held is a service kept on its previous version after its new one
// failed.
type Held struct {
	Service    string
	Commit     string // the commit whose version failed
	Reason     string
	Since      time.Time
	Image      string          // image ID it's pinned to; "" when it was never deployed
	Definition json.RawMessage // its previous compose definition; nil when never deployed
}

// Event is something the deployer did, for the status and a future
// notification service.
type Event struct {
	Time    time.Time
	Kind    string
	Commit  string `json:",omitempty"`
	Service string `json:",omitempty"`
	Message string
}

// Deploying marks a deploy in progress, so one cut short can be resumed.
type Deploying struct{ From, To string }

// State is .deploy/state.json.
type State struct {
	Version  int
	Deployed struct {
		Commit, Subject string
		At              time.Time
	}
	LastResult string
	Skipped    []string
	Held       map[string]Held
	Events     []Event // newest last
	CertExpiry time.Time
	LastRun    map[string]time.Time // scheduled one-shots
	Deploying  *Deploying
}

// Load reads dir/state.json; a missing file is an empty state.
func Load(dir string) (*State, error) {
	s := &State{Version: 1}
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("state.json: %w", err)
	}
	return s, nil
}

// Save writes dir/state.json atomically (a crash leaves the old file).
func (s *State) Save(dir string) error {
	s.Version = 1
	b, err := json.Marshal(s) // compact: indenting would reformat stored definitions
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "state.json"), b)
}

// Add records an event, keeping the newest 50.
func (s *State) Add(e Event) {
	s.Events = append(s.Events, e)
	if len(s.Events) > maxEvents {
		s.Events = s.Events[len(s.Events)-maxEvents:]
	}
}

// IsSkipped is true for a commit that must never be deployed.
func (s *State) IsSkipped(sha string) bool { return slices.Contains(s.Skipped, sha) }

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
