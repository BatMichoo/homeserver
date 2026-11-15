package backup

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/BatMichoo/homeserver/deploy/internal/compose"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

const complete = "\n-- PostgreSQL database dump complete\n\n"

type fakeExec struct {
	running bool
	dumps   map[string]string // db -> dump
	calls   [][]string
}

func (f *fakeExec) Running(ctx context.Context, svc string) (bool, error) { return f.running, nil }

func (f *fakeExec) Exec(ctx context.Context, svc string, stdin io.Reader, cmd ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{svc}, cmd...))
	switch cmd[0] {
	case "psql":
		var names []string
		for n := range f.dumps {
			names = append(names, n)
		}
		sort.Strings(names)
		return []byte(strings.Join(names, "\n") + "\n"), nil
	case "pg_dump":
		d, ok := f.dumps[cmd[len(cmd)-1]]
		if !ok {
			return nil, errors.New("no such database")
		}
		return []byte(d), nil
	}
	return nil, errors.New("unexpected")
}

func project(label string) compose.Project {
	return compose.Project{Services: map[string]compose.Service{
		"db":  {Name: "db", Labels: map[string]string{"homeserver.backup": label}},
		"web": {Name: "web", Labels: map[string]string{}},
	}}
}

func gunzip(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	r, err := gzip.NewReader(f)
	require.NoError(t, err)
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(b)
}

func TestBackupDumpsEachDatabase(t *testing.T) {
	dir := t.TempDir()
	x := &fakeExec{running: true, dumps: map[string]string{"auth": "AUTH" + complete, "expenser": "EXP" + complete}}
	files, err := Run(context.Background(), x, project("postgres"), dir, "abcdef1234", 14, t0)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, filepath.Join(dir, "db-auth-20261115-100000-abcdef1.sql.gz"), files[0])
	assert.Equal(t, "AUTH"+complete, gunzip(t, files[0]))
	assert.Equal(t, "EXP"+complete, gunzip(t, files[1]))
	assert.Equal(t, []string{"db", "pg_dump", "-U", "postgres", "auth"}, x.calls[1])
}

func TestBackupTruncatedFails(t *testing.T) {
	dir := t.TempDir()
	x := &fakeExec{running: true, dumps: map[string]string{"auth": "half a dump"}}
	_, err := Run(context.Background(), x, project("postgres"), dir, "abc1234", 14, t0)
	assert.Error(t, err)
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries, "no partial file left")
}

func TestBackupNotRunningSkips(t *testing.T) {
	x := &fakeExec{running: false}
	files, err := Run(context.Background(), x, project("postgres"), t.TempDir(), "abc1234", 14, t0)
	require.NoError(t, err)
	assert.Empty(t, files)
	assert.Empty(t, x.calls)
}

func TestBackupKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	x := &fakeExec{running: true, dumps: map[string]string{"auth": "A" + complete}}
	for i := 0; i < 4; i++ {
		_, err := Run(context.Background(), x, project("postgres"), dir, "abc1234", 2, t0.Add(time.Duration(i)*time.Hour))
		require.NoError(t, err)
	}
	// Another database's files aren't touched.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "db-other-20200101-000000-abc1234.sql.gz"), nil, 0o644))
	_, err := Run(context.Background(), x, project("postgres"), dir, "abc1234", 2, t0.Add(5*time.Hour))
	require.NoError(t, err)
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Equal(t, []string{
		"db-auth-20261115-130000-abc1234.sql.gz",
		"db-auth-20261115-150000-abc1234.sql.gz",
		"db-other-20200101-000000-abc1234.sql.gz",
	}, names)
}

func TestBackupUnknownKind(t *testing.T) {
	_, err := Run(context.Background(), &fakeExec{running: true}, project("mysql"), t.TempDir(), "abc1234", 14, t0)
	assert.ErrorContains(t, err, "mysql")
}

// Pruning one database's dumps never touches another whose name starts
// the same way.
func TestBackupPruneExactDatabase(t *testing.T) {
	dir := t.TempDir()
	x := &fakeExec{running: true, dumps: map[string]string{"app": "A" + complete, "app-old": "O" + complete}}
	for i := 0; i < 3; i++ {
		_, err := Run(context.Background(), x, project("postgres"), dir, "abc1234", 1, t0.Add(time.Duration(i)*time.Hour))
		require.NoError(t, err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Equal(t, []string{
		"db-app-20261115-120000-abc1234.sql.gz",
		"db-app-old-20261115-120000-abc1234.sql.gz",
	}, names)
}
