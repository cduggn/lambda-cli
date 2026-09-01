package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/cduggn/lambda-cli/internal/config"
	"github.com/spf13/cobra"
)

// syncOpts are the knobs push/pull expose on top of rsync's defaults.
type syncOpts struct {
	dryRun   bool
	delete   bool
	excludes []string
	extra    []string // passed through verbatim after --
}

// sshTransport is rsync's -e value. rsync splits it on whitespace, so a key path
// containing a space cannot be expressed here; callers warn about that.
func sshTransport(cfg *config.Config) string {
	return fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=accept-new", cfg.SSHPrivateKey)
}

// rsyncArgs builds the full argv (excluding the program name).
func rsyncArgs(cfg *config.Config, o syncOpts, src, dst string) []string {
	args := []string{"-avz", "--progress"}
	if o.dryRun {
		args = append(args, "-n")
	}
	if o.delete {
		args = append(args, "--delete")
	}
	for _, e := range o.excludes {
		args = append(args, "--exclude", e)
	}
	args = append(args, "-e", sshTransport(cfg))
	args = append(args, o.extra...)
	return append(args, src, dst)
}

// remoteSpec renders rsync's user@host:path form.
func remoteSpec(cfg *config.Config, ip, path string) string {
	return fmt.Sprintf("%s@%s:%s", cfg.SSHUser, ip, path)
}

// defaultRemoteDir maps a local source to ~/<basename>/ on the instance, so
// `lam push` from ~/work/class7 lands in ~/class7 the way the class scripts do.
func defaultRemoteDir(src string) string {
	abs, err := filepath.Abs(src)
	if err != nil {
		abs = src
	}
	base := filepath.Base(strings.TrimSuffix(abs, string(filepath.Separator)))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "~/"
	}
	return "~/" + base + "/"
}

// splitPassthrough separates the command's own args from anything after "--".
func splitPassthrough(cmd *cobra.Command, args []string) (own, extra []string) {
	dash := cmd.ArgsLenAtDash()
	if dash < 0 {
		return args, nil
	}
	return args[:dash], args[dash:]
}

func newPush(app *App) *cobra.Command {
	var o syncOpts
	var target string
	var excludes []string
	cmd := &cobra.Command{
		Use:   "push [SRC] [DEST]",
		Short: "rsync a local directory to the instance",
		Long: `Copy a local directory to the instance over rsync, using the configured ssh key.

SRC defaults to the current directory and DEST to ~/<basename of SRC>, so running
"lam push" inside class7 puts it in ~/class7 on the box.

Trailing slashes carry rsync's usual meaning: "src/" copies the contents of src,
"src" copies the directory itself. Anything after -- is passed straight to rsync.`,
		Example: `  lam push
  lam push ./class7 '~/class7/'
  lam push -n                   # dry run
  lam push -- --delete`,
		RunE: func(cmd *cobra.Command, args []string) error {
			own, extra := splitPassthrough(cmd, args)
			if len(own) > 2 {
				return fmt.Errorf("expected at most SRC and DEST, got %d arguments", len(own))
			}
			o.extra = extra
			c, cfg, err := app.Client()
			if err != nil {
				return err
			}
			src := "."
			if len(own) > 0 {
				src = own[0]
			}
			if _, err := os.Stat(src); err != nil {
				return fmt.Errorf("local source %s: %w", src, err)
			}
			dst := defaultRemoteDir(src)
			if len(own) > 1 {
				dst = own[1]
			}
			inst, err := resolveInstance(cmd.Context(), c, target)
			if err != nil {
				return err
			}
			if inst.IP == "" {
				return fmt.Errorf("instance %s has no IP yet (status=%s); try: lam wait", inst.ID, inst.Status)
			}
			o.excludes = pickExcludes(cfg, cmd, excludes)
			return runRsync(cfg, o, src, remoteSpec(cfg, inst.IP, dst))
		},
	}
	bindSyncFlags(cmd, &o, &target, &excludes)
	return cmd
}

func newPull(app *App) *cobra.Command {
	var o syncOpts
	var target string
	var excludes []string
	cmd := &cobra.Command{
		Use:   "pull SRC [DEST]",
		Short: "rsync a directory from the instance to your machine",
		Long: `Copy a remote path from the instance to your machine over rsync.

SRC is a path on the instance. DEST defaults to the current directory.
Anything after -- is passed straight to rsync.`,
		Example: `  lam pull '~/class7/results' .
  lam pull '~/class7/results.json'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			own, extra := splitPassthrough(cmd, args)
			if len(own) < 1 {
				return fmt.Errorf("pull needs a remote SRC path, e.g. lam pull '~/class7/results'")
			}
			if len(own) > 2 {
				return fmt.Errorf("expected at most SRC and DEST, got %d arguments", len(own))
			}
			o.extra = extra
			c, cfg, err := app.Client()
			if err != nil {
				return err
			}
			dst := "."
			if len(own) > 1 {
				dst = own[1]
			}
			inst, err := resolveInstance(cmd.Context(), c, target)
			if err != nil {
				return err
			}
			if inst.IP == "" {
				return fmt.Errorf("instance %s has no IP yet (status=%s); try: lam wait", inst.ID, inst.Status)
			}
			o.excludes = pickExcludes(cfg, cmd, excludes)
			return runRsync(cfg, o, remoteSpec(cfg, inst.IP, own[0]), dst)
		},
	}
	bindSyncFlags(cmd, &o, &target, &excludes)
	return cmd
}

func bindSyncFlags(cmd *cobra.Command, o *syncOpts, target *string, excludes *[]string) {
	f := cmd.Flags()
	f.StringVarP(target, "instance", "i", "", "instance id or name (default: the only running one)")
	f.BoolVarP(&o.dryRun, "dry-run", "n", false, "show what would transfer, change nothing")
	f.BoolVar(&o.delete, "delete", false, "delete files at the destination that are gone from the source")
	f.StringSliceVar(excludes, "exclude", nil, "exclude pattern, repeatable (replaces the configured list)")
}

// pickExcludes prefers --exclude when given, else the configured list.
func pickExcludes(cfg *config.Config, cmd *cobra.Command, flagVal []string) []string {
	if cmd.Flags().Changed("exclude") {
		return flagVal
	}
	return cfg.Exclude
}

// displayArgs renders argv for humans, quoting any element containing whitespace
// so the echoed line can be copy-pasted (the -e transport is a single argument).
func displayArgs(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t") {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			out[i] = a
		}
	}
	return strings.Join(out, " ")
}

func runRsync(cfg *config.Config, o syncOpts, src, dst string) error {
	path, err := exec.LookPath("rsync")
	if err != nil {
		return fmt.Errorf("rsync not found on PATH: %w", err)
	}
	if strings.ContainsAny(cfg.SSHPrivateKey, " \t") {
		return fmt.Errorf("ssh key path %q contains whitespace, which rsync's -e cannot express; move the key or symlink it", cfg.SSHPrivateKey)
	}
	args := rsyncArgs(cfg, o, src, dst)
	logf("rsync %s", displayArgs(args))
	return syscall.Exec(path, append([]string{"rsync"}, args...), os.Environ())
}
