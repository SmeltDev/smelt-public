package gitutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/smeltdev/smelt/internal/brand"
	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/filecache"
	"github.com/smeltdev/smelt/internal/gitexec"
	"github.com/smeltdev/smelt/internal/gitremote"
)

// Source says which step of DefaultBranch produced an answer.
type Source string

// The sources, in the order DefaultBranch tries them.
const (
	SourceConfig     Source = "config"
	SourceHost       Source = "host"
	SourceOriginHead Source = "origin-head"
	SourceRemote     Source = "remote"
	SourceOnlyBranch Source = "only-branch"
)

// Branch is a repository's default branch and where the answer came from.
type Branch struct {
	Name string
	// Remote is the remote the branch lives on, "" when none is known.
	Remote string
	Source Source
}

// DefaultHost names a repository's default branch, as the code host records it.
// gitutil never runs a host tool itself; the caller supplies one.
type DefaultHost interface {
	DefaultBranch(ctx context.Context) (string, error)
}

// Opts configures DefaultBranch. The zero value uses only local git state.
type Opts struct {
	// Host, when set, is asked before git is. Nil skips the step.
	Host DefaultHost
	// DataDir is smelt's data folder for the project. The remote step caches
	// under it; empty disables that cache.
	DataDir string
	// Now is the clock for cache expiry, time.Now when nil.
	Now func() time.Time
}

func (o Opts) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// ErrNoDefaultBranch means no step could name the default branch.
var ErrNoDefaultBranch = fmt.Errorf(
	`the repository's default branch is unknown; set "vcs": {"default_branch": "<name>"} in %s/config.json`,
	brand.ProjectDir)

const (
	// hostTimeout and remoteTimeout bound the network steps, so a slow network
	// never holds up the caller.
	hostTimeout   = 10 * time.Second
	remoteTimeout = 10 * time.Second
	// remoteTTL is how long an ls-remote answer is trusted.
	remoteTTL = 24 * time.Hour
	// remoteCacheDir is the folder under DataDir holding those answers.
	remoteCacheDir = "default-branch-remote"
)

// remotes are the remotes tried, in order.
const originRemote = "origin"

var remotes = []string{originRemote, "upstream"}

type step func(ctx context.Context, dir string, o Opts) (Branch, bool)

var steps = []step{configStep, hostStep, originHeadStep, remoteStep, onlyBranchStep}

// DefaultBranch finds the branch a PR into dir's repository merges into. It
// tries the config, the code host, the remote HEAD git recorded, the remote
// itself and the branch names, and stops at the first answer. It never guesses
// and never writes to the repository: no answer is ErrNoDefaultBranch.
func DefaultBranch(ctx context.Context, dir string, o Opts) (Branch, error) {
	for _, s := range steps {
		if b, ok := s(ctx, dir, o); ok {
			return b, nil
		}
	}
	return Branch{}, ErrNoDefaultBranch
}

// firstRemote is the first of origin and upstream that exists in dir.
func firstRemote(ctx context.Context, dir string) string {
	for _, r := range remotes {
		if _, err := gitremote.URL(ctx, dir, r); err == nil {
			return r
		}
	}
	return ""
}

func configStep(ctx context.Context, dir string, _ Opts) (Branch, bool) {
	cfg, err := config.LoadProjectConfig(dir)
	if err != nil || cfg.VCS == nil || cfg.VCS.DefaultBranch == "" {
		return Branch{}, false
	}
	return Branch{Name: cfg.VCS.DefaultBranch, Remote: firstRemote(ctx, dir), Source: SourceConfig}, true
}

func hostStep(ctx context.Context, _ string, o Opts) (Branch, bool) {
	if o.Host == nil {
		return Branch{}, false
	}
	hctx, cancel := context.WithTimeout(ctx, hostTimeout)
	defer cancel()
	name, err := o.Host.DefaultBranch(hctx)
	if err != nil || name == "" {
		return Branch{}, false
	}
	return Branch{Name: name, Remote: originRemote, Source: SourceHost}, true
}

func originHeadStep(_ context.Context, dir string, _ Opts) (Branch, bool) {
	remote, name, err := originHead(dir)
	if err != nil {
		return Branch{}, false
	}
	return Branch{Name: name, Remote: remote, Source: SourceOriginHead}, true
}

// remoteEntry is a cached ls-remote answer. The cache ignores age, so the entry
// carries its own timestamp.
type remoteEntry struct {
	Branch    string    `json:"branch"`
	FetchedAt time.Time `json:"fetchedAt"`
}

func remoteStep(ctx context.Context, dir string, o Opts) (Branch, bool) {
	for _, r := range remotes {
		if name := remoteDefault(ctx, dir, r, o); name != "" {
			return Branch{Name: name, Remote: r, Source: SourceRemote}, true
		}
	}
	return Branch{}, false
}

// remoteDefault is the default branch of one remote, "" when it cannot be
// learned. It reads the cache first and writes only under DataDir.
func remoteDefault(ctx context.Context, dir, remote string, o Opts) string {
	url, err := gitremote.URL(ctx, dir, remote)
	if err != nil {
		return ""
	}
	var cache filecache.FileCache[remoteEntry]
	if o.DataDir != "" {
		cache = filecache.FileCache[remoteEntry]{Dir: filepath.Join(o.DataDir, "cache", remoteCacheDir), MaxAge: remoteTTL}
		if e, ok := cache.Get(url); ok && e.Branch != "" && o.now().Sub(e.FetchedAt) < remoteTTL {
			return e.Branch
		}
	}
	name := lsRemoteHead(ctx, dir, remote)
	if name != "" && o.DataDir != "" {
		// A cache that cannot be written only costs the next lookup.
		_ = cache.Put(url, remoteEntry{Branch: name, FetchedAt: o.now()})
	}
	return name
}

// lsRemoteHead asks the remote which branch its HEAD names.
func lsRemoteHead(ctx context.Context, dir, remote string) string {
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	// Never a credential prompt: there is no terminal to answer one.
	runner := gitexec.Runner{Dir: dir, Env: noPromptEnv(os.Environ())}
	out, err := runner.Output(ctx, "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return ""
	}
	return parseSymref(string(out))
}

// parseSymref reads the branch from `ref: refs/heads/<name>\tHEAD`.
func parseSymref(out string) string {
	for _, line := range strings.Split(out, "\n") {
		ref, target, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && target == "HEAD" && strings.HasPrefix(ref, "ref: refs/heads/") {
			return strings.TrimPrefix(ref, "ref: refs/heads/")
		}
	}
	return ""
}

func onlyBranchStep(ctx context.Context, dir string, _ Opts) (Branch, bool) {
	var found []string
	for _, name := range []string{"main", "master"} {
		_, err := (gitexec.Runner{Dir: dir}).Output(ctx, "rev-parse", "--verify", "--quiet", "refs/remotes/"+originRemote+"/"+name)
		if err == nil {
			found = append(found, name)
		}
	}
	if len(found) != 1 {
		return Branch{}, false
	}
	return Branch{Name: found[0], Remote: originRemote, Source: SourceOnlyBranch}, true
}
