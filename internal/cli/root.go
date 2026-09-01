// Package cli wires the lam commands.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/cduggn/lambda-cli/internal/config"
	"github.com/cduggn/lambda-cli/internal/lambda"
	"github.com/spf13/cobra"
)

// App holds lazily-built dependencies shared by commands.
type App struct {
	Version string
	cfg     *config.Config
	client  *lambda.Client
}

func (a *App) Config() (*config.Config, error) {
	if a.cfg == nil {
		c, err := config.Load()
		if err != nil {
			return nil, err
		}
		a.cfg = c
	}
	return a.cfg, nil
}

func (a *App) Client() (*lambda.Client, *config.Config, error) {
	cfg, err := a.Config()
	if err != nil {
		return nil, nil, err
	}
	if !cfg.KeySet() {
		return nil, nil, fmt.Errorf("LAMBDA_API_KEY not set (env or %s). Create one at https://cloud.lambda.ai/api-keys, then: lam config init", cfg.Path)
	}
	if a.client == nil {
		a.client = lambda.New(cfg.APIBase, cfg.APIKey)
	}
	return a.client, cfg, nil
}

func logf(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }

func table(rows [][]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	w.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func usd(t lambda.InstanceType) string { return fmt.Sprintf("%.2f", t.PriceUSD()) }

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// statePath is where `launch`/`env` record the last instance (sourceable by the class .env scripts).
func statePath() string {
	if d := os.Getenv("LAM_STATE_DIR"); d != "" {
		return filepath.Join(d, "last")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "lam", "last")
}

func envLines(cfg *config.Config, inst lambda.Instance) string {
	return fmt.Sprintf("export LAMBDA_ID=%s\nexport LAMBDA=%s@%s\nexport LAMBDA_SSH_KEY=%s\n", inst.ID, cfg.SSHUser, inst.IP, cfg.SSHPrivateKey)
}

func saveState(cfg *config.Config, inst lambda.Instance) {
	p := statePath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte(envLines(cfg, inst)), 0o600)
}

// resolveInstance picks an instance by id, id prefix, or name; empty = the only non-terminated one.
func resolveInstance(ctx context.Context, c *lambda.Client, q string) (lambda.Instance, error) {
	list, err := c.Instances(ctx)
	if err != nil {
		return lambda.Instance{}, err
	}
	var live []lambda.Instance
	for _, i := range list {
		if i.Status != lambda.StatusTerminated {
			live = append(live, i)
		}
	}
	if q == "" {
		switch len(live) {
		case 0:
			return lambda.Instance{}, fmt.Errorf("no instances. Try: lam launch")
		case 1:
			return live[0], nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d instances, pick one:", len(live))
		for _, i := range live {
			fmt.Fprintf(&b, "\n  %s  %s  %s  %s", i.ID, orDash(i.Name), i.Status, orDash(i.IP))
		}
		return lambda.Instance{}, fmt.Errorf("%s", b.String())
	}
	for _, i := range list {
		if i.ID == q || i.Name == q {
			return i, nil
		}
	}
	for _, i := range list {
		if strings.HasPrefix(i.ID, q) {
			return i, nil
		}
	}
	return lambda.Instance{}, fmt.Errorf("no instance matching %q", q)
}

// NewRoot builds the command tree.
func NewRoot(version string) *cobra.Command {
	app := &App{Version: version}
	root := &cobra.Command{
		Use:           "lam",
		Short:         "Lambda Cloud from the terminal: launch, bootstrap, ssh, terminate",
		Long:          "lam drives the Lambda Cloud REST API. Defaults come from env or ~/.config/lam/config.\nLAMBDA_API_KEY is required for anything that talks to the API.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newLaunch(app), newLs(app), newSSH(app), newPush(app), newPull(app), newEnv(app), newWait(app), newLogs(app), newRm(app),
		newTypes(app), newImages(app), newKeys(app), newRender(app), newConfig(app),
	)
	return root
}

// Execute runs the CLI with Ctrl-C cancelling in-flight waits.
func Execute(version string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := NewRoot(version).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "lam:", err)
		return 1
	}
	return 0
}
