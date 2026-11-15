package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/spf13/cobra"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/factory"
	"github.com/smeltdev/smelt/internal/gitutil"
	"github.com/smeltdev/smelt/internal/iostream"
)

func newRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Ask about the repository smelt is working in",
	}
	cmd.AddCommand(newRepoDefaultBranchCmd())
	return cmd
}

func newRepoDefaultBranchCmd() *cobra.Command {
	var projectDir string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "default-branch",
		Short: "Print the repository's default branch and where the answer came from",
		Long: `Print the branch a pull request into this repository merges into.

The answer is the first of: vcs.default_branch in .smelt/config.json, the code
host (gh), the remote HEAD git recorded, the remote itself, the only branch
there is. Host and remote answers are cached in smelt's data folder. Nothing is
written to the repository and nothing is guessed: when no step answers, the
command exits 1 and says how to set vcs.default_branch.

With --json the output is {"branch","remote","source"}, source being one of
config, host, origin-head, remote or only-branch.`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveHookRoot(cmd.Context(), projectDir)
			if err != nil {
				return err
			}
			dataDir, _ := config.ProjectDataDir(root)
			host := factory.DefaultBranchHostFor(root, dataDir)
			return runRepoDefaultBranch(cmd.Context(), iostream.FromCmd(cmd), root, host, dataDir, jsonOut)
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "Override project directory")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the answer as JSON")
	return cmd
}

// defaultBranchJSON is the --json body. remote is never omitted.
type defaultBranchJSON struct {
	Branch string `json:"branch"`
	Remote string `json:"remote"`
	Source string `json:"source"`
}

// renderDefaultBranch renders an answer, or the error when there is none.
func renderDefaultBranch(b gitutil.Branch, err error, jsonOut bool) string {
	var v any
	switch {
	case err != nil && jsonOut:
		v = map[string]string{"error": err.Error()}
	case err != nil:
		return err.Error() + "\n"
	case jsonOut:
		v = defaultBranchJSON{Branch: b.Name, Remote: b.Remote, Source: string(b.Source)}
	default:
		return b.Name + "\n"
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return buf.String()
}

// runRepoDefaultBranch prints the answer to stdout, or the failure to stderr
// with exit code 1, so main adds nothing of its own.
func runRepoDefaultBranch(ctx context.Context, streams iostream.Streams, root string, host gitutil.DefaultHost, dataDir string, jsonOut bool) error {
	b, err := gitutil.DefaultBranch(ctx, root, gitutil.Opts{Host: host, DataDir: dataDir})
	if err != nil {
		if !errors.Is(err, gitutil.ErrNoDefaultBranch) {
			return err
		}
		streams.ErrPrintf("%s", renderDefaultBranch(b, err, jsonOut))
		return exitCodeError{code: 1}
	}
	streams.Printf("%s", renderDefaultBranch(b, nil, jsonOut))
	return nil
}
