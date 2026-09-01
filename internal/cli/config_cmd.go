package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/cduggn/lambda-cli/internal/cloudinit"
	"github.com/cduggn/lambda-cli/internal/config"
	"github.com/spf13/cobra"
)

func newConfig(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use: "config", Short: "Show the effective configuration", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := app.Config()
			if err != nil {
				return err
			}
			file := "(absent; run: lam config init)"
			if cfg.FileExists {
				file = "(present)"
			}
			key := "NOT SET — set LAMBDA_API_KEY in env or " + cfg.Path
			if cfg.KeySet() {
				key = "set (" + cfg.APIKey[:min(6, len(cfg.APIKey))] + "…)"
			}
			pk := ""
			if _, err := os.Stat(cfg.SSHPrivateKey); err != nil {
				pk = " (MISSING)"
			}
			fmt.Printf(`config file:          %s %s
LAMBDA_API_KEY:       %s
LAM_REGION:           %s
LAM_TYPE:             %s
LAM_SSH_KEY:          %s
LAM_SSH_PRIVATE_KEY:  %s%s
LAM_IMAGE_FAMILY:     %s
LAM_CLOUD_INIT:       %s
templates:            %s   (user dir: %s)
`, cfg.Path, file, key, cfg.Region, cfg.Type, cfg.SSHKey, cfg.SSHPrivateKey, pk, cfg.ImageFamily, orNone(cfg.CloudInit),
				strings.Join(cloudinit.Names(cfg.TemplatesDir), " "), cfg.TemplatesDir)
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use: "init", Short: "Create the config file from the example", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := config.DefaultPath()
			if err := config.WriteExample(p); err != nil {
				return err
			}
			logf("wrote %s — edit it and set LAMBDA_API_KEY", p)
			return nil
		},
	})
	return cmd
}

func newRender(app *App) *cobra.Command {
	return &cobra.Command{
		Use: "render NAME|FILE", Short: "Print a cloud-init template with {{VARS}} filled from the environment", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := app.Config()
			if err != nil {
				return err
			}
			out, from, missing, err := cloudinit.Load(args[0], cfg.TemplatesDir, os.LookupEnv)
			if err != nil {
				return err
			}
			for _, m := range missing {
				logf("warning: {{%s}} unset (rendered empty)", m)
			}
			logf("# source: %s", from)
			fmt.Print(out)
			return nil
		},
	}
}
