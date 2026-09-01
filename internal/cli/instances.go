package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cduggn/lambda-cli/internal/lambda"
	"github.com/spf13/cobra"
)

func newLs(app *App) *cobra.Command {
	var uptime bool
	var since time.Duration
	cmd := &cobra.Command{
		Use: "ls", Aliases: []string{"list", "ps"}, Short: "List instances and the current $/hr burn rate", Args: cobra.NoArgs,
		Long: `List instances with their hourly rate and the total burn rate.

With --uptime, add how long each instance has been up and roughly what it has
cost so far. Lambda's instance objects carry no launch timestamp, so this reads
the account activity log, which is one extra API call. Spend is an estimate from
elapsed time and the hourly rate, not a billing figure.`,
		Example: "  lam ls\n  lam ls -u\n  lam ls -u --since 90d",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, _, err := app.Client()
			if err != nil {
				return err
			}
			list, err := c.Instances(ctx)
			if err != nil {
				return err
			}

			starts := map[string]time.Time{}
			auditFailed := false
			if uptime {
				starts, err = c.InstanceStartTimes(ctx, time.Now().Add(-since))
				if err != nil {
					// The listing is still useful without it; don't fail the command.
					logf("warning: could not read the activity log for uptime (%v)", err)
					auditFailed = true
				}
			}

			header := []string{"ID", "NAME", "TYPE", "REGION", "STATUS", "IP", "$/HR"}
			if uptime {
				header = append(header, "UPTIME", "SPENT")
			}
			rows := [][]string{header}
			var burn, spent float64
			unknown := 0
			now := time.Now()
			for _, i := range list {
				running := i.Status == lambda.StatusActive || i.Status == lambda.StatusBooting
				row := []string{i.ID, orDash(i.Name), i.InstanceType.Name, i.Region.Name, i.Status, orDash(i.IP), usd(i.InstanceType)}
				if uptime {
					up, cost := "-", "-"
					if running {
						if start, ok := starts[i.ID]; ok {
							d := now.Sub(start)
							up = shortDur(d)
							c := d.Hours() * i.InstanceType.PriceUSD()
							cost = fmt.Sprintf("%.2f", c)
							spent += c
						} else {
							up, cost = "?", "?"
							unknown++
						}
					}
					row = append(row, up, cost)
				}
				if running {
					burn += i.InstanceType.PriceUSD()
				}
				rows = append(rows, row)
			}
			table(rows)
			fmt.Printf("\nburn rate: $%.2f/hr across running instances\n", burn)
			// With no launch times there is no spend to report, and the warning above
			// already said why; a "$0.00" line would read as a real figure.
			if uptime && !auditFailed {
				fmt.Printf("spent so far: $%.2f (estimate from the activity log, not a bill)\n", spent)
				if unknown > 0 {
					logf("note: %d instance(s) had no launch event in the last %s; try a longer --since", unknown, shortDur(since))
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&uptime, "uptime", "u", false, "add uptime and estimated spend (one extra API call)")
	cmd.Flags().DurationVar(&since, "since", 30*24*time.Hour, "how far back to search the activity log for launch events")
	return cmd
}

// shortDur renders a duration compactly: 3d4h, 5h12m, 42m.
func shortDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	// Check before rounding: Round(time.Minute) turns 30s into 1m.
	if d < time.Minute {
		return "<1m"
	}
	d = d.Round(time.Minute)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0 && hours == 0:
		return fmt.Sprintf("%dd", days)
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0 && mins == 0:
		return fmt.Sprintf("%dh", hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

func newSSH(app *App) *cobra.Command {
	return &cobra.Command{
		Use: "ssh [ID|NAME] [-- CMD...]", Short: "ssh into an instance (no arg = the only running one)",
		Example: "  lam ssh\n  lam ssh lab -- nvidia-smi",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, cfg, err := app.Client()
			if err != nil {
				return err
			}
			q, rest := splitTarget(cmd, args)
			inst, err := resolveInstance(cmd.Context(), c, q)
			if err != nil {
				return err
			}
			if inst.IP == "" {
				return fmt.Errorf("instance %s has no IP yet (status=%s); try: lam wait", inst.ID, inst.Status)
			}
			return sshExec(cfg, inst.IP, rest...)
		},
	}
}

// splitTarget separates an optional instance selector from a trailing "-- cmd".
func splitTarget(cmd *cobra.Command, args []string) (string, []string) {
	dash := cmd.ArgsLenAtDash()
	if dash < 0 {
		if len(args) > 0 {
			return args[0], nil
		}
		return "", nil
	}
	q := ""
	if dash > 0 {
		q = args[0]
	}
	return q, args[dash:]
}

func newEnv(app *App) *cobra.Command {
	return &cobra.Command{
		Use: "env [ID|NAME]", Short: "Print export LAMBDA=… lines (matches the class .env format)", Args: cobra.MaximumNArgs(1),
		Example: "  eval \"$(lam env)\"\n  lam env >> class7/.env",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, cfg, err := app.Client()
			if err != nil {
				return err
			}
			inst, err := resolveInstance(cmd.Context(), c, first(args))
			if err != nil {
				return err
			}
			if inst.IP == "" {
				return fmt.Errorf("instance %s has no IP yet (status=%s); try: lam wait", inst.ID, inst.Status)
			}
			saveState(cfg, inst)
			fmt.Print(envLines(cfg, inst))
			return nil
		},
	}
}

func newWait(app *App) *cobra.Command {
	return &cobra.Command{
		Use: "wait [ID|NAME]", Short: "Block until the instance is active, sshd is up, and cloud-init has finished", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, cfg, err := app.Client()
			if err != nil {
				return err
			}
			inst, err := resolveInstance(ctx, c, first(args))
			if err != nil {
				return err
			}
			if inst.Status != lambda.StatusActive || inst.IP == "" {
				inst, err = c.WaitActive(ctx, inst.ID, 10*time.Second, cfg.ActiveTimeout, func(status, ip string, el time.Duration) {
					logf("  status=%s ip=%s (%s)", status, orDash(ip), el.Round(time.Second))
				})
				if err != nil {
					return err
				}
			}
			if err := waitSSH(ctx, cfg, inst.IP, cfg.SSHTimeout); err != nil {
				return err
			}
			saveState(cfg, inst)
			_ = waitCloudInit(ctx, cfg, inst.IP)
			fmt.Println(sshCommandLine(cfg, inst.IP))
			return nil
		},
	}
}

func newLogs(app *App) *cobra.Command {
	return &cobra.Command{
		Use: "logs [ID|NAME]", Aliases: []string{"log"}, Short: "Tail /var/log/cloud-init-output.log on the instance", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, cfg, err := app.Client()
			if err != nil {
				return err
			}
			inst, err := resolveInstance(cmd.Context(), c, first(args))
			if err != nil {
				return err
			}
			if inst.IP == "" {
				return fmt.Errorf("instance %s has no IP yet", inst.ID)
			}
			return sshExec(cfg, inst.IP, "sudo tail -n 50 -f /var/log/cloud-init-output.log")
		},
	}
}

func newRm(app *App) *cobra.Command {
	var yes, all bool
	cmd := &cobra.Command{
		Use: "rm [ID|NAME...]", Aliases: []string{"terminate", "down", "kill"}, Short: "Terminate instances (no arg = the only running one)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, _, err := app.Client()
			if err != nil {
				return err
			}
			var targets []lambda.Instance
			switch {
			case all:
				list, err := c.Instances(ctx)
				if err != nil {
					return err
				}
				for _, i := range list {
					if i.Status != lambda.StatusTerminated && i.Status != lambda.StatusTerminating {
						targets = append(targets, i)
					}
				}
			case len(args) == 0:
				i, err := resolveInstance(ctx, c, "")
				if err != nil {
					return err
				}
				targets = append(targets, i)
			default:
				for _, q := range args {
					i, err := resolveInstance(ctx, c, q)
					if err != nil {
						return err
					}
					targets = append(targets, i)
				}
			}
			if len(targets) == 0 {
				logf("nothing to terminate")
				return nil
			}
			logf("terminate:")
			ids := make([]string, 0, len(targets))
			for _, i := range targets {
				logf("  %s  %s  %s  %s", i.ID, orDash(i.Name), i.InstanceType.Name, orDash(i.IP))
				ids = append(ids, i.ID)
			}
			if !yes {
				fmt.Fprint(os.Stderr, "confirm [y/N] ")
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if l := strings.ToLower(strings.TrimSpace(line)); l != "y" && l != "yes" {
					return fmt.Errorf("aborted")
				}
			}
			done, err := c.Terminate(ctx, ids)
			if err != nil {
				return err
			}
			for _, i := range done {
				fmt.Printf("terminating %s %s\n", i.ID, i.Name)
			}
			_ = os.Remove(statePath())
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "terminate every non-terminated instance")
	return cmd
}

func first(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}
