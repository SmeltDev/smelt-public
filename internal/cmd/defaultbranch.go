package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/factory"
	"github.com/smeltdev/smelt/internal/gitutil"
)

// defaultBranchLine says which branch PRs merge into and where that answer came
// from. ok is false when the branch is unknown: the line is then a warning that
// names the fix. configured is a vcs.default_branch not yet on disk, "" for none.
func defaultBranchLine(ctx context.Context, root, configured string, host gitutil.DefaultHost) (line string, ok bool) {
	if configured != "" {
		return formatDefaultBranch(gitutil.Branch{Name: configured, Source: gitutil.SourceConfig}), true
	}
	dataDir, _ := config.ProjectDataDir(root)
	b, err := gitutil.DefaultBranch(ctx, root, gitutil.Opts{Host: host, DataDir: dataDir})
	if err != nil {
		if !errors.Is(err, gitutil.ErrNoDefaultBranch) {
			return fmt.Sprintf("default branch: unknown: %v", err), false
		}
		return err.Error(), false
	}
	return formatDefaultBranch(b), true
}

// projectDefaultBranchLine is defaultBranchLine with the GitHub host factory runs use.
func projectDefaultBranchLine(ctx context.Context, root, configured string) (string, bool) {
	var host gitutil.DefaultHost
	if configured == "" {
		dataDir, _ := config.ProjectDataDir(root)
		if h := factory.DefaultBranchHostFor(root, dataDir); h != nil {
			host = h
		}
	}
	return defaultBranchLine(ctx, root, configured, host)
}

func formatDefaultBranch(b gitutil.Branch) string {
	return fmt.Sprintf("default branch: %s (from %s)", b.Name, branchSource(b))
}

func branchSource(b gitutil.Branch) string {
	switch b.Source {
	case gitutil.SourceConfig:
		return "vcs.default_branch in .smelt/config.json"
	case gitutil.SourceHost:
		return "the code host"
	case gitutil.SourceOriginHead:
		remote := b.Remote
		if remote == "" {
			remote = "origin"
		}
		return remote + "/HEAD"
	case gitutil.SourceRemote:
		return "the remote"
	case gitutil.SourceOnlyBranch:
		return "the only origin branch"
	}
	return string(b.Source)
}
