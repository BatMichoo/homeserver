package state

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"time"
)

const statusEvents = 20

// status is public/status.json: what the dashboard (and a future
// notification service) reads. Version 1; nothing secret in it.
type status struct {
	Version  int `json:"version"`
	Deployed struct {
		Commit  string    `json:"commit"`
		Subject string    `json:"subject"`
		At      time.Time `json:"at"`
	} `json:"deployed"`
	LastResult string        `json:"last_result"`
	Held       []statusHeld  `json:"held"`
	CertExpiry *time.Time    `json:"cert_expiry"`
	Events     []statusEvent `json:"events"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

type statusHeld struct {
	Service string    `json:"service"`
	Commit  string    `json:"commit"`
	Since   time.Time `json:"since"`
	Reason  string    `json:"reason"`
}

type statusEvent struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Commit  string    `json:"commit,omitempty"`
	Service string    `json:"service,omitempty"`
	Message string    `json:"message"`
}

// WriteStatus writes dir/public/status.json from s, as of now.
func WriteStatus(dir string, s *State, now time.Time) error {
	st := status{Version: 1, LastResult: s.LastResult, Held: []statusHeld{}, Events: []statusEvent{}, UpdatedAt: now.UTC()}
	st.Deployed.Commit, st.Deployed.Subject, st.Deployed.At = s.Deployed.Commit, s.Deployed.Subject, s.Deployed.At
	if !s.CertExpiry.IsZero() {
		e := s.CertExpiry
		st.CertExpiry = &e
	}
	var names []string
	for n := range s.Held {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		h := s.Held[n]
		st.Held = append(st.Held, statusHeld{Service: n, Commit: h.Commit, Since: h.Since, Reason: h.Reason})
	}
	events := s.Events
	if len(events) > statusEvents {
		events = events[len(events)-statusEvents:]
	}
	for _, e := range events {
		st.Events = append(st.Events, statusEvent(e))
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "public", "status.json"), b, 0o644)
}
