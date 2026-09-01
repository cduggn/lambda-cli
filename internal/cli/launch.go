package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cduggn/lambda-cli/internal/cloudinit"
	"github.com/cduggn/lambda-cli/internal/lambda"
	"github.com/spf13/cobra"
)

func newLaunch(app *App) *cobra.Command {
	var (
		typ, region, key, family, imageID, name, hostname, cloudInit string
		noCloudInit, noWait, noWaitInit, dryRun                      bool
		retry                                                        time.Duration
	)
	cmd := &cobra.Command{
		Use:   "launch",
		Short: "Launch an instance, wait for ssh + cloud-init, print the ssh command",
		Long: `Launch an instance with the configured defaults, then wait for it to boot, for sshd
to accept your key, and for cloud-init (if a template was given) to finish.
Template {{VARS}} are filled from your environment (e.g. HF_TOKEN, MODEL).`,
		Example: `  lam launch -c vllm --name lab
  lam launch --dry-run
  lam launch --retry 30m
  HF_TOKEN=hf_x MODEL=Qwen/Qwen3-0.6B lam launch -c vllm`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, cfg, err := app.Client()
			if err != nil {
				return err
			}
			if typ == "" {
				typ = cfg.Type
			}
			if region == "" {
				region = cfg.Region
			}
			if key == "" {
				key = cfg.SSHKey
			}
			if family == "" && imageID == "" {
				family = cfg.ImageFamily
			}
			if imageID != "" {
				family = ""
			}
			if cloudInit == "" && !noCloudInit {
				cloudInit = cfg.CloudInit
			}
			if noCloudInit {
				cloudInit = ""
			}

			// Preflight: cheap checks that save a failed launch call.
			types, err := c.InstanceTypes(ctx)
			if err != nil {
				return err
			}
			t, ok := types[typ]
			if !ok {
				return fmt.Errorf("unknown instance type %q. See: lam types", typ)
			}
			keys, err := c.SSHKeys(ctx)
			if err != nil {
				return err
			}
			if !hasKey(keys, key) {
				return fmt.Errorf("ssh key %q is not registered in Lambda. See: lam keys  (add: lam keys add NAME ~/.ssh/x.pub)", key)
			}
			if _, err := os.Stat(cfg.SSHPrivateKey); err != nil {
				logf("warning: private key %s not found; ssh/wait steps will fail", cfg.SSHPrivateKey)
			}

			req := lambda.LaunchRequest{RegionName: region, InstanceTypeName: typ, SSHKeyNames: []string{key}, Name: name, Hostname: hostname}
			switch {
			case imageID != "":
				req.Image = &lambda.ImageSpec{ID: imageID}
			case family != "":
				req.Image = &lambda.ImageSpec{Family: family}
			}
			if cloudInit != "" {
				rendered, from, missing, err := cloudinit.Load(cloudInit, cfg.TemplatesDir, os.LookupEnv)
				if err != nil {
					return err
				}
				for _, m := range missing {
					logf("warning: template var {{%s}} is unset in your environment (rendered empty)", m)
				}
				req.UserData = rendered
				cloudInit = from
			}

			img := family
			if imageID != "" {
				img = imageID
			}
			logf("launch: %s in %s  image=%s  key=%s  cloud-init=%s  $%s/hr", typ, region, img, key, orNone(cloudInit), usd(t.InstanceType))
			if !t.HasCapacity(region) {
				logf("note: Lambda currently reports no %s capacity in %s (has: %s)", typ, region, orDash(regionNames(t)))
			}
			if dryRun {
				show := req
				if show.UserData != "" {
					show.UserData = fmt.Sprintf("<%d bytes>", len(req.UserData))
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetEscapeHTML(false)
				enc.SetIndent("", "  ")
				return enc.Encode(show)
			}

			ids, err := c.LaunchRetry(ctx, req, retry, 15*time.Second, func(err error, until time.Time) {
				logf("  no %s capacity in %s (%s); retrying in 15s until %s", typ, region, time.Now().Format("15:04:05"), until.Format("15:04:05"))
			})
			if err != nil {
				if lambda.IsCode(err, lambda.CodeInsufficientCapacity) {
					return fmt.Errorf("%w\n  tip: lam types -a   (where capacity is)   or   lam launch --retry 30m", err)
				}
				return err
			}
			id := ids[0]
			logf("launched instance %s", id)
			if noWait {
				fmt.Println(id)
				return nil
			}

			start := time.Now()
			inst, err := c.WaitActive(ctx, id, 10*time.Second, cfg.ActiveTimeout, func(status, ip string, el time.Duration) {
				logf("  status=%s ip=%s (%s)", status, orDash(ip), el.Round(time.Second))
			})
			if err != nil {
				return fmt.Errorf("%w. Check: lam ls", err)
			}
			saveState(cfg, inst)
			logf("active: %s  (booted in %s)", sshTarget(cfg, inst.IP), time.Since(start).Round(time.Second))

			sshStart := time.Now()
			if err := waitSSH(ctx, cfg, inst.IP, cfg.SSHTimeout); err != nil {
				return err
			}
			logf("ssh ready (%s)", time.Since(sshStart).Round(time.Second))

			if req.UserData != "" && !noWaitInit {
				logf("waiting for cloud-init (%s) … tail with: lam logs %s", cloudInit, id)
				if err := waitCloudInit(ctx, cfg, inst.IP); err == nil {
					logf("cloud-init done")
				}
			}
			logf("\nready in %s.   ssh: lam ssh    env: eval \"$(lam env)\"    kill: lam rm %s", time.Since(start).Round(time.Second), id)
			fmt.Println(sshCommandLine(cfg, inst.IP))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&typ, "type", "t", "", "instance type (default from config)")
	f.StringVarP(&region, "region", "r", "", "region (default from config)")
	f.StringVarP(&key, "key", "k", "", "Lambda ssh key name (default from config)")
	f.StringVarP(&family, "image-family", "f", "", "image family (default from config)")
	f.StringVar(&imageID, "image-id", "", "specific image id (overrides family)")
	f.StringVarP(&name, "name", "n", "", "instance name")
	f.StringVar(&hostname, "hostname", "", "instance hostname")
	f.StringVarP(&cloudInit, "cloud-init", "c", "", "cloud-init template name or file path")
	f.BoolVar(&noCloudInit, "no-cloud-init", false, "ignore the LAM_CLOUD_INIT default")
	f.DurationVar(&retry, "retry", 0, "keep retrying on insufficient capacity for this long, e.g. 30m")
	f.BoolVar(&noWait, "no-wait", false, "print the instance id and return immediately")
	f.BoolVar(&noWaitInit, "no-wait-init", false, "wait for ssh but not for cloud-init")
	f.BoolVar(&dryRun, "dry-run", false, "print the launch request and exit")
	return cmd
}

func hasKey(keys []lambda.SSHKey, name string) bool {
	for _, k := range keys {
		if k.Name == name {
			return true
		}
	}
	return false
}

func regionNames(t lambda.InstanceTypeItem) string {
	var names []string
	for _, r := range t.RegionsWithCapacityAvailable {
		names = append(names, r.Name)
	}
	return strings.Join(names, ",")
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
