package git

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BatMichoo/homeserver/deploy/internal/run"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

// sh runs git in dir for test setup.
func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_ALLOW_PROTOCOL=file")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// setup makes a bare "remote", a "dev" clone that pushes to it, and a
// "server" clone the deployer works in.
func setup(t *testing.T) (dev, server, remote string, g Git) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	dev = filepath.Join(root, "dev")
	server = filepath.Join(root, "server")
	sh(t, root, "init", "-q", "--bare", "-b", "main", remote)
	sh(t, root, "clone", "-q", remote, dev)
	write(t, filepath.Join(dev, "README.md"), "hi\n")
	write(t, filepath.Join(dev, ".gitignore"), ".deploy/\n")
	sh(t, dev, "add", "-A")
	sh(t, dev, "commit", "-qm", "first")
	sh(t, dev, "push", "-q", "origin", "main")
	sh(t, root, "clone", "-q", remote, server)
	return dev, server, "file://" + remote, Git{R: run.Exec{}, Dir: server}
}

func commit(t *testing.T, dev, file, content, msg string) string {
	t.Helper()
	write(t, filepath.Join(dev, file), content)
	sh(t, dev, "add", "-A")
	sh(t, dev, "commit", "-qm", msg)
	sh(t, dev, "push", "-q", "origin", "main")
	return sh(t, dev, "rev-parse", "HEAD")
}

func TestGitFlow(t *testing.T) {
	dev, server, remote, g := setup(t)
	branch, err := g.Branch(ctx)
	require.NoError(t, err)
	assert.Equal(t, "main", branch)
	old, err := g.Head(ctx)
	require.NoError(t, err)

	newSHA := commit(t, dev, "auth/x.go", "package x\n", "change auth")
	got, err := g.Fetch(ctx, remote, "main")
	require.NoError(t, err)
	assert.Equal(t, newSHA, got)

	ok, err := g.IsAncestor(ctx, old, newSHA)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, _ = g.IsAncestor(ctx, newSHA, old)
	assert.False(t, ok)

	files, err := g.ChangedFiles(ctx, old, newSHA)
	require.NoError(t, err)
	assert.Equal(t, []string{"auth/x.go"}, files)
	subject, err := g.Subject(ctx, newSHA)
	require.NoError(t, err)
	assert.Equal(t, "change auth", subject)

	require.NoError(t, g.FastForward(ctx, newSHA))
	head, _ := g.Head(ctx)
	assert.Equal(t, newSHA, head)

	dirty, err := g.Dirty(ctx)
	require.NoError(t, err)
	assert.False(t, dirty)
	write(t, filepath.Join(server, ".deploy", "state.json"), "{}")
	dirty, _ = g.Dirty(ctx)
	assert.False(t, dirty, "ignored files don't count")
	write(t, filepath.Join(server, "stray.txt"), "x")
	dirty, _ = g.Dirty(ctx)
	assert.True(t, dirty, "untracked file counts")
	require.NoError(t, os.Remove(filepath.Join(server, "stray.txt")))

	require.NoError(t, g.ResetKeep(ctx, old))
	head, _ = g.Head(ctx)
	assert.Equal(t, old, head)
	require.NoError(t, g.SubmoduleUpdate(ctx))
}

func TestGitSubmoduleBumpIsChangedPath(t *testing.T) {
	dev, server, remote, g := setup(t)
	// An app repo, added to dev as a submodule.
	app := filepath.Join(filepath.Dir(dev), "app")
	sh(t, filepath.Dir(dev), "init", "-q", "-b", "main", app)
	write(t, filepath.Join(app, "main.go"), "v1\n")
	sh(t, app, "add", "-A")
	sh(t, app, "commit", "-qm", "v1")
	sh(t, dev, "-c", "protocol.file.allow=always", "submodule", "add", "-q", app, "apps/app")
	sh(t, dev, "commit", "-qm", "add app")
	sh(t, dev, "push", "-q", "origin", "main")

	before, err := g.Fetch(ctx, remote, "main")
	require.NoError(t, err)
	require.NoError(t, g.FastForward(ctx, before))
	g.R = fileProtocol{run.Exec{}}
	require.NoError(t, g.SubmoduleUpdate(ctx))
	assert.FileExists(t, filepath.Join(server, "apps/app/main.go"))

	write(t, filepath.Join(app, "main.go"), "v2\n")
	sh(t, app, "commit", "-qam", "v2")
	sh(t, filepath.Join(dev, "apps/app"), "pull", "-q", "origin", "main")
	sh(t, dev, "commit", "-qam", "bump app")
	sh(t, dev, "push", "-q", "origin", "main")

	after, err := g.Fetch(ctx, remote, "main")
	require.NoError(t, err)
	files, err := g.ChangedFiles(ctx, before, after)
	require.NoError(t, err)
	assert.Equal(t, []string{"apps/app"}, files)
}

// fileProtocol lets submodules from local paths be fetched in tests (git
// refuses file:// submodules by default).
type fileProtocol struct{ run.Exec }

func (f fileProtocol) Run(ctx context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	if name == "git" {
		args = append([]string{"-c", "protocol.file.allow=always"}, args...)
	}
	return f.Exec.Run(ctx, dir, stdin, name, args...)
}

// Removing a submodule leaves its directory behind in a checkout that had
// it; RemoveStray clears it so the checkout isn't "modified" forever.
// Tracked paths and ignored files are left alone.
func TestRemoveStraySubmoduleLeftovers(t *testing.T) {
	dev, server, remote, g := setup(t)
	g.R = fileProtocol{run.Exec{}}
	app := filepath.Join(filepath.Dir(dev), "app")
	sh(t, filepath.Dir(dev), "init", "-q", "-b", "main", app)
	write(t, filepath.Join(app, "main.go"), "v1\n")
	sh(t, app, "add", "-A")
	sh(t, app, "commit", "-qm", "v1")
	sh(t, dev, "-c", "protocol.file.allow=always", "submodule", "add", "-q", app, "apps/app")
	sh(t, dev, "commit", "-qm", "add app")
	sh(t, dev, "push", "-q", "origin", "main")
	added, err := g.Fetch(ctx, remote, "main")
	require.NoError(t, err)
	require.NoError(t, g.FastForward(ctx, added))
	require.NoError(t, g.SubmoduleUpdate(ctx))

	sh(t, dev, "rm", "-q", "apps/app")
	sh(t, dev, "commit", "-qm", "drop app")
	sh(t, dev, "push", "-q", "origin", "main")
	removed, err := g.Fetch(ctx, remote, "main")
	require.NoError(t, err)
	require.NoError(t, g.FastForward(ctx, removed))
	write(t, filepath.Join(server, ".deploy", "keep.json"), "{}")

	dirty, _ := g.Dirty(ctx)
	require.True(t, dirty, "the leftover submodule directory shows as untracked")
	require.NoError(t, g.RemoveStray(ctx, []string{"apps/app", "README.md", "gone/file"}))
	dirty, _ = g.Dirty(ctx)
	assert.False(t, dirty)
	assert.NoDirExists(t, filepath.Join(server, "apps", "app"))
	assert.FileExists(t, filepath.Join(server, "README.md"), "tracked files untouched")
	assert.FileExists(t, filepath.Join(server, ".deploy", "keep.json"), "ignored files untouched")
}
