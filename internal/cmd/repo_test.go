package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/smeltdev/smelt/internal/gitutil"
	"github.com/smeltdev/smelt/internal/iostream"
)

func repoGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	gitIn(t, dir, append([]string{"-c", "user.email=t@example.com", "-c", "user.name=T"}, args...)...)
}

// repoWithRemote is a repo on master with origin a bare repo whose HEAD is
// master, master pushed, and no origin/HEAD recorded.
func repoWithRemote(t *testing.T) string {
	t.Helper()
	remote := t.TempDir()
	repoGit(t, remote, "init", "--bare", "-b", "master")
	dir := t.TempDir()
	repoGit(t, dir, "init", "-b", "master")
	repoGit(t, dir, "commit", "--allow-empty", "-m", "init")
	repoGit(t, dir, "remote", "add", "origin", remote)
	repoGit(t, dir, "push", "origin", "master")
	return dir
}

func runRepoCase(t *testing.T, dir string, host gitutil.DefaultHost, jsonOut bool) (stdout, stderr string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	err = runRepoDefaultBranch(context.Background(), iostream.Streams{Out: &out, Err: &errb},
		dir, host, t.TempDir(), jsonOut)
	return out.String(), errb.String(), err
}

func TestRepoDefaultBranchGoldens(t *testing.T) {
	cases := map[string]func(t *testing.T) (string, gitutil.DefaultHost){
		"config": func(t *testing.T) (string, gitutil.DefaultHost) {
			dir := repoWithRemote(t)
			assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".smelt"), 0o755))
			assert.NilError(t, os.WriteFile(filepath.Join(dir, ".smelt", "config.json"),
				[]byte(`{"vcs": {"default_branch": "master"}}`), 0o644))
			return dir, nil
		},
		"host": func(t *testing.T) (string, gitutil.DefaultHost) {
			return repoWithRemote(t), fakeBranchHost{branch: "master"}
		},
		"origin-head": func(t *testing.T) (string, gitutil.DefaultHost) {
			dir := repoWithRemote(t)
			repoGit(t, dir, "update-ref", "refs/remotes/origin/master", "HEAD")
			repoGit(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/master")
			return dir, nil
		},
		"remote": func(t *testing.T) (string, gitutil.DefaultHost) {
			return repoWithRemote(t), nil
		},
		"only-branch": func(t *testing.T) (string, gitutil.DefaultHost) {
			dir := t.TempDir()
			repoGit(t, dir, "init", "-b", "main")
			repoGit(t, dir, "commit", "--allow-empty", "-m", "init")
			repoGit(t, dir, "remote", "add", "origin", filepath.Join(dir, "nowhere"))
			repoGit(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
			return dir, nil
		},
	}
	for name, setup := range cases {
		for _, jsonOut := range []bool{false, true} {
			golden := "repo-default-branch-" + name + ".golden"
			if jsonOut {
				golden += ".json"
			}
			t.Run(golden, func(t *testing.T) {
				dir, host := setup(t)
				out, errOut, err := runRepoCase(t, dir, host, jsonOut)
				assert.NilError(t, err)
				assert.Equal(t, errOut, "")
				want, rerr := os.ReadFile(filepath.Join("testdata", golden))
				assert.NilError(t, rerr)
				assert.Equal(t, out, string(want))
			})
		}
	}
}

func TestRepoDefaultBranchUnknown(t *testing.T) {
	for _, jsonOut := range []bool{false, true} {
		golden := "repo-default-branch-unknown.golden"
		if jsonOut {
			golden += ".json"
		}
		t.Run(golden, func(t *testing.T) {
			dir := t.TempDir()
			repoGit(t, dir, "init", "-b", "trunk")
			repoGit(t, dir, "commit", "--allow-empty", "-m", "init")
			out, errOut, err := runRepoCase(t, dir, nil, jsonOut)
			var ec exitCodeError
			assert.Assert(t, errors.As(err, &ec))
			assert.Equal(t, ec.ExitCode(), 1)
			assert.Equal(t, out, "")
			want, rerr := os.ReadFile(filepath.Join("testdata", golden))
			assert.NilError(t, rerr)
			assert.Equal(t, errOut, string(want))
		})
	}
}

func TestRepoDefaultBranchOutsideARepoIsUnknown(t *testing.T) {
	_, errOut, err := runRepoCase(t, t.TempDir(), nil, false)
	var ec exitCodeError
	assert.Assert(t, errors.As(err, &ec))
	assert.Equal(t, ec.ExitCode(), 1)
	assert.Check(t, cmp.Contains(errOut, "default branch is unknown"))
}

func TestRepoDefaultBranchCachesTheRemoteAnswer(t *testing.T) {
	dir := repoWithRemote(t)
	data := t.TempDir()
	for range 2 {
		var out bytes.Buffer
		assert.NilError(t, runRepoDefaultBranch(context.Background(),
			iostream.Streams{Out: &out, Err: &out}, dir, nil, data, false))
		assert.Equal(t, out.String(), "master\n")
	}
	entries, err := os.ReadDir(filepath.Join(data, "cache", "default-branch-remote"))
	assert.NilError(t, err)
	assert.Check(t, len(entries) > 0)
}

// TestRepoDefaultBranchThroughTheRootCommand drives the registered command, so
// the registration, the --json and --project flags and the host wiring are all
// exercised.
func TestRepoDefaultBranchThroughTheRootCommand(t *testing.T) {
	isolateConfig(t)
	// A PATH holding only git keeps the real gh out of the host step.
	gitPath, err := exec.LookPath("git")
	assert.NilError(t, err)
	bin := t.TempDir()
	assert.NilError(t, os.Symlink(gitPath, filepath.Join(bin, "git")))
	t.Setenv("PATH", bin)

	dir := repoWithRemote(t)
	for _, jsonOut := range []bool{false, true} {
		golden := "repo-default-branch-remote.golden"
		args := []string{"repo", "default-branch", "--project", dir}
		if jsonOut {
			golden += ".json"
			args = append(args, "--json")
		}
		t.Run(golden, func(t *testing.T) {
			var out, errb bytes.Buffer
			root := newTestRootCmd()
			root.SetOut(&out)
			root.SetErr(&errb)
			root.SetArgs(args)
			assert.NilError(t, root.ExecuteContext(context.Background()))
			want, rerr := os.ReadFile(filepath.Join("testdata", golden))
			assert.NilError(t, rerr)
			assert.Equal(t, out.String(), string(want))
			assert.Equal(t, errb.String(), "")
		})
	}
}
