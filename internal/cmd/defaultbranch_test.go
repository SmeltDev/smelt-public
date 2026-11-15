package cmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/golden"
)

type fakeBranchHost struct {
	branch string
	err    error
}

func (f fakeBranchHost) DefaultBranch(context.Context) (string, error) { return f.branch, f.err }

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// branchFixture makes a repo on main with origin a bare remote whose HEAD is
// trunk, and main and trunk pushed. No origin/HEAD is recorded.
func branchFixture(t *testing.T) string {
	t.Helper()
	remote := t.TempDir()
	gitIn(t, remote, "init", "--bare", "-b", "trunk")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-b", "main")
	gitIn(t, dir, "config", "user.email", "test@example.com")
	gitIn(t, dir, "config", "user.name", "Test")
	gitIn(t, dir, "commit", "--allow-empty", "-m", "init")
	gitIn(t, dir, "remote", "add", "origin", remote)
	gitIn(t, dir, "push", "origin", "main:refs/heads/main", "main:refs/heads/trunk")
	return dir
}

// branchCases are the fixtures, one per source and one unknown.
var branchCases = []struct {
	name  string
	setup func(t *testing.T, dir string) (host fakeBranchHost, useHost bool)
}{
	{"config", func(t *testing.T, dir string) (fakeBranchHost, bool) {
		assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".smelt"), 0o755))
		assert.NilError(t, os.WriteFile(filepath.Join(dir, ".smelt", "config.json"), []byte(`{"vcs": {"default_branch": "develop"}}`), 0o644))
		return fakeBranchHost{}, false
	}},
	{"host", func(*testing.T, string) (fakeBranchHost, bool) { return fakeBranchHost{branch: "hostb"}, true }},
	{"origin-head", func(t *testing.T, dir string) (fakeBranchHost, bool) {
		gitIn(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/headb")
		return fakeBranchHost{}, false
	}},
	{"upstream-head", func(t *testing.T, dir string) (fakeBranchHost, bool) {
		gitIn(t, dir, "remote", "rename", "origin", "upstream")
		gitIn(t, dir, "symbolic-ref", "refs/remotes/upstream/HEAD", "refs/remotes/upstream/devel")
		return fakeBranchHost{}, false
	}},
	{"remote", func(*testing.T, string) (fakeBranchHost, bool) { return fakeBranchHost{}, false }},
	{"only-branch", func(t *testing.T, dir string) (fakeBranchHost, bool) {
		gitIn(t, dir, "remote", "set-url", "origin", filepath.Join(dir, "nowhere"))
		return fakeBranchHost{}, false
	}},
	{"unknown", func(t *testing.T, dir string) (fakeBranchHost, bool) {
		gitIn(t, dir, "remote", "remove", "origin")
		return fakeBranchHost{}, false
	}},
}

func TestDefaultBranchLineGolden(t *testing.T) {
	var got strings.Builder
	for _, tc := range branchCases {
		isolateConfig(t)
		dir := branchFixture(t)
		host, useHost := tc.setup(t, dir)
		var line string
		if useHost {
			line, _ = defaultBranchLine(t.Context(), dir, "", host)
		} else {
			line, _ = defaultBranchLine(t.Context(), dir, "", nil)
		}
		got.WriteString(tc.name + ": " + line + "\n")
	}
	golden.Assert(t, got.String(), "default-branch-lines.golden")
}

func TestDefaultBranchLineHostFailureFallsThrough(t *testing.T) {
	isolateConfig(t)
	dir := branchFixture(t)
	line, ok := defaultBranchLine(t.Context(), dir, "", fakeBranchHost{err: errors.New("gh: not logged in")})
	assert.Assert(t, ok)
	assert.Equal(t, line, "default branch: trunk (from the remote)")
}

func TestDefaultBranchLineUnknownNamesTheKey(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-b", "main")
	line, ok := defaultBranchLine(t.Context(), dir, "", nil)
	assert.Assert(t, !ok)
	assert.Assert(t, strings.Contains(line, `"default_branch"`), line)
}
