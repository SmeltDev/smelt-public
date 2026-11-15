package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

type fakeHost struct {
	branch string
	err    error
}

func (f fakeHost) DefaultBranch(context.Context) (string, error) { return f.branch, f.err }

// bareRemote makes a bare repo whose HEAD names head, whether or not that
// branch exists. -b is explicit because init.defaultBranch would otherwise
// decide it.
func bareRemote(t *testing.T, head string) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "--bare", "-b", head)
	return dir
}

// pushedRepo makes a repo on branch with origin set to remote and every named
// branch pushed. It records no origin/HEAD.
func pushedRepo(t *testing.T, remote, branch string, push ...string) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", branch)
	gitRun(t, dir, "commit", "--allow-empty", "-m", "init")
	gitRun(t, dir, "remote", "add", "origin", remote)
	for _, b := range push {
		gitRun(t, dir, "push", "origin", branch+":refs/heads/"+b)
	}
	return dir
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".smelt"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".smelt", "config.json"), []byte(body), 0o644))
}

func TestDefaultBranchPrecedence(t *testing.T) {
	// Every source answers with a different name; each case removes the ones
	// above it.
	cases := []struct {
		name                                  string
		config, host, originHead, breakRemote bool
		want, wantRemote                      string
		wantSource                            Source
	}{
		{"config wins", true, true, true, false, "cfg", "origin", SourceConfig},
		{"host over git", false, true, true, false, "hostb", "origin", SourceHost},
		{"origin/HEAD over remote", false, false, true, false, "headb", "origin", SourceOriginHead},
		{"remote over branch names", false, false, false, false, "trunk", "origin", SourceRemote},
		{"only branch", false, false, false, true, "main", "origin", SourceOnlyBranch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remote := bareRemote(t, "trunk")
			dir := pushedRepo(t, remote, "main", "main", "trunk")
			var o Opts
			if tc.config {
				writeConfig(t, dir, `{"vcs": {"default_branch": "cfg"}}`)
			}
			if tc.host {
				o.Host = fakeHost{branch: "hostb"}
			}
			if tc.originHead {
				gitRun(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/headb")
			}
			if tc.breakRemote {
				gitRun(t, dir, "remote", "set-url", "origin", filepath.Join(dir, "nowhere"))
			}
			got, err := DefaultBranch(context.Background(), dir, o)
			assert.NilError(t, err)
			assert.DeepEqual(t, got, Branch{Name: tc.want, Remote: tc.wantRemote, Source: tc.wantSource})
		})
	}
}

func TestDefaultBranchConfigEdgeCases(t *testing.T) {
	remote := bareRemote(t, "trunk")
	dir := pushedRepo(t, remote, "main", "trunk")
	for name, body := range map[string]string{
		"empty key":   `{"vcs": {}}`,
		"parse error": `{not json`,
	} {
		writeConfig(t, dir, body)
		got, err := DefaultBranch(context.Background(), dir, Opts{})
		assert.NilError(t, err, name)
		assert.Check(t, cmp.Equal(got.Source, SourceRemote), name)
	}
}

func TestDefaultBranchHostFailureFallsThrough(t *testing.T) {
	remote := bareRemote(t, "trunk")
	dir := pushedRepo(t, remote, "main", "trunk")
	got, err := DefaultBranch(context.Background(), dir, Opts{Host: fakeHost{err: context.DeadlineExceeded}})
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(got.Source, SourceRemote))
}

func TestDefaultBranchCloneWithOriginHead(t *testing.T) {
	remote := bareRemote(t, "trunk")
	pushedRepo(t, remote, "main", "trunk")
	clone := t.TempDir()
	gitRun(t, clone, "clone", remote, "work")
	got, err := DefaultBranch(context.Background(), filepath.Join(clone, "work"), Opts{})
	assert.NilError(t, err)
	assert.DeepEqual(t, got, Branch{Name: "trunk", Remote: "origin", Source: SourceOriginHead})
}

func TestDefaultBranchPushedRepoResolvesFromRemoteAndWritesNothing(t *testing.T) {
	remote := bareRemote(t, "master")
	dir := pushedRepo(t, remote, "master", "master")
	data := t.TempDir()

	got, err := DefaultBranch(context.Background(), dir, Opts{DataDir: data})
	assert.NilError(t, err)
	assert.DeepEqual(t, got, Branch{Name: "master", Remote: "origin", Source: SourceRemote})

	_, err = os.Stat(filepath.Join(dir, ".git", "refs", "remotes", "origin", "HEAD"))
	assert.Check(t, os.IsNotExist(err), "the resolve must not record origin/HEAD")
	_, err = os.Stat(filepath.Join(dir, ".smelt"))
	assert.Check(t, os.IsNotExist(err))
}

func TestDefaultBranchBothNamesWithNoHeadIsUnknown(t *testing.T) {
	remote := bareRemote(t, "elsewhere") // HEAD names a branch that does not exist
	dir := pushedRepo(t, remote, "main", "main", "master")

	_, err := DefaultBranch(context.Background(), dir, Opts{})
	assert.Check(t, cmp.ErrorIs(err, ErrNoDefaultBranch))
}

func TestDefaultBranchForkUsesUpstreamHead(t *testing.T) {
	dir := setupRepo(t)
	setRemoteHead(t, dir, "upstream", "trunk")
	got, err := DefaultBranch(context.Background(), dir, Opts{})
	assert.NilError(t, err)
	assert.DeepEqual(t, got, Branch{Name: "trunk", Remote: "upstream", Source: SourceOriginHead})
}

func TestDefaultBranchNoRemoteIsAnErrorNotMain(t *testing.T) {
	dir := setupRepo(t)
	_, err := DefaultBranch(context.Background(), dir, Opts{})
	assert.Check(t, cmp.ErrorIs(err, ErrNoDefaultBranch))
}

func TestErrNoDefaultBranchNamesTheConfigKey(t *testing.T) {
	assert.Check(t, cmp.ErrorContains(ErrNoDefaultBranch, `"vcs": {"default_branch":`))
	assert.Check(t, cmp.ErrorContains(ErrNoDefaultBranch, ".smelt/config.json"))
}

func TestDefaultBranchRemoteAnswerIsCachedForADay(t *testing.T) {
	remote := bareRemote(t, "master")
	dir := pushedRepo(t, remote, "master", "master")
	data := t.TempDir()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	o := Opts{DataDir: data, Now: func() time.Time { return now }}

	got, err := DefaultBranch(context.Background(), dir, o)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(got.Source, SourceRemote))

	// With the remote gone, only the cache can answer.
	assert.NilError(t, os.RemoveAll(remote))
	now = now.Add(23 * time.Hour)
	got, err = DefaultBranch(context.Background(), dir, o)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(got.Source, SourceRemote), "a fresh entry must answer without ls-remote")

	// Expired: ls-remote runs again, fails, and the next step answers.
	now = now.Add(2 * time.Hour)
	got, err = DefaultBranch(context.Background(), dir, o)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(got.Source, SourceOnlyBranch), "an expired entry must be asked again")
}
