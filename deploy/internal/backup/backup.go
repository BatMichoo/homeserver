// Package backup dumps databases before a deploy. Which services hold
// databases comes from the label homeserver.backup (only "postgres" so
// far), never from service names.
package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BatMichoo/homeserver/deploy/internal/compose"
)

// Label marks a service whose databases are dumped before each deploy.
const Label = "homeserver.backup"

// Execer runs commands in a compose service (compose.Client).
type Execer interface {
	Exec(ctx context.Context, service string, stdin io.Reader, cmd ...string) ([]byte, error)
	Running(ctx context.Context, service string) (bool, error)
}

// Run dumps every database of every labelled, running service into dir as
// <service>-<db>-<time>-<commit7>.sql.gz, keeping the newest keep per
// database. It returns the files written. A labelled service that isn't
// running (first deploy) is skipped.
func Run(ctx context.Context, x Execer, p compose.Project, dir, commit string, keep int, now time.Time) ([]string, error) {
	var written []string
	for _, name := range p.Order() {
		kind, ok := p.Services[name].Labels[Label]
		if !ok {
			continue
		}
		if kind != "postgres" {
			return written, fmt.Errorf("service %s: %s %q: only \"postgres\" is supported", name, Label, kind)
		}
		running, err := x.Running(ctx, name)
		if err != nil {
			return written, err
		}
		if !running {
			continue
		}
		files, err := postgres(ctx, x, name, dir, commit, keep, now)
		written = append(written, files...)
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func postgres(ctx context.Context, x Execer, service, dir, commit string, keep int, now time.Time) ([]string, error) {
	out, err := x.Exec(ctx, service, nil, "psql", "-U", "postgres", "-tAc",
		"SELECT datname FROM pg_database WHERE NOT datistemplate AND datname <> 'postgres' ORDER BY datname")
	if err != nil {
		return nil, fmt.Errorf("list databases in %s: %w", service, err)
	}
	if len(commit) > 7 {
		commit = commit[:7]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var written []string
	for _, db := range strings.Fields(string(out)) {
		dump, err := x.Exec(ctx, service, nil, "pg_dump", "-U", "postgres", db)
		if err != nil {
			return written, fmt.Errorf("dump %s/%s: %w", service, db, err)
		}
		// A dump cut short (disk full, database stopped) lacks this line.
		if !bytes.Contains(lastBytes(dump, 200), []byte("PostgreSQL database dump complete")) {
			return written, fmt.Errorf("dump %s/%s is incomplete", service, db)
		}
		prefix := service + "-" + db + "-"
		path := filepath.Join(dir, prefix+now.UTC().Format("20060102-150405")+"-"+commit+".sql.gz")
		if err := writeGzip(path, dump); err != nil {
			return written, err
		}
		written = append(written, path)
		if err := prune(dir, prefix, keep); err != nil {
			return written, err
		}
	}
	return written, nil
}

func lastBytes(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}

func writeGzip(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	_, err = zw.Write(data)
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// dumpName matches <prefix><YYYYMMDD-HHMMSS>-<commit>.sql.gz, so pruning
// "db-app-" never touches "db-app-old-…".
var dumpName = regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]+\.sql\.gz$`)

// prune keeps the newest keep dumps of one database (names sort by time).
func prune(dir, prefix string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var mine []string
	for _, e := range entries {
		if rest, ok := strings.CutPrefix(e.Name(), prefix); ok && dumpName.MatchString(rest) {
			mine = append(mine, e.Name())
		}
	}
	sort.Strings(mine)
	for len(mine) > keep {
		if err := os.Remove(filepath.Join(dir, mine[0])); err != nil {
			return err
		}
		mine = mine[1:]
	}
	return nil
}
