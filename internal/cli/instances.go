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
	return &cobra.Command{
		Use: "ls", Aliases: []string{"list", "ps"}, Short: "List instances and the current $/hr burn rate", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, err := app.Client()
			if err != nil {
				return err
			}
			list, err := c.Instances(cmd.Context())
			if err != nil {
				return err
			}
			rows := [][]string{{"ID", "NAME", "TYPE", "REGION", "STATUS", "IP", "$/HR"}}
			burn := 0.0
			for _, i := range list {
				rows = append(rows, []string{i.ID, orDash(i.Name), i.InstanceType.Name, i.Region.Name, i.Status, orDash(i.IP), usd(i.InstanceType)})
				if i.Status == lambda.StatusActive || i.Status == lambda.StatusBooting {
					burn += i.InstanceType.PriceUSD()
				}
			}
			table(rows)
			fmt.Printf("\nburn rate: $%.2f/hr across running instances\n", burn)
			return nil
		},
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
