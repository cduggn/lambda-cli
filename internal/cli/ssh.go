package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/cduggn/lambda-cli/internal/config"
)

func sshBaseArgs(cfg *config.Config) []string {
	return []string{"-i", cfg.SSHPrivateKey, "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=8", "-o", "ServerAliveInterval=30"}
}

func sshTarget(cfg *config.Config, ip string) string { return cfg.SSHUser + "@" + ip }

func sshCommandLine(cfg *config.Config, ip string) string {
	return fmt.Sprintf("ssh -i %s %s", cfg.SSHPrivateKey, sshTarget(cfg, ip))
}

// sshReachable runs `ssh ... true` non-interactively.
func sshReachable(ctx context.Context, cfg *config.Config, ip string) bool {
	args := append(sshBaseArgs(cfg), "-o", "BatchMode=yes", sshTarget(cfg, ip), "true")
	return exec.CommandContext(ctx, "ssh", args...).Run() == nil
}

// waitSSH polls until sshd accepts our key or timeout.
func waitSSH(ctx context.Context, cfg *config.Config, ip string, timeout time.Duration) error {
	start := time.Now()
	for !sshReachable(ctx, cfg, ip) {
		if time.Since(start) > timeout {
			return fmt.Errorf("ssh not reachable after %s: %s", timeout.Round(time.Second), sshCommandLine(cfg, ip))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return nil
}

// sshRun runs a remote command with our stdio attached (not a tty).
func sshRun(ctx context.Context, cfg *config.Config, ip, remote string) error {
	args := append(sshBaseArgs(cfg), sshTarget(cfg, ip), remote)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// sshExec replaces the current process with an interactive ssh (so the tty is handed over cleanly).
func sshExec(cfg *config.Config, ip string, extra ...string) error {
	path, err := exec.LookPath("ssh")
	if err != nil {
		return err
	}
	argv := append([]string{"ssh"}, sshBaseArgs(cfg)...)
	argv = append(argv, "-t", sshTarget(cfg, ip))
	argv = append(argv, extra...)
	return syscall.Exec(path, argv, os.Environ())
}

// waitCloudInit blocks until cloud-init finishes on the box; on error, shows the log tail.
func waitCloudInit(ctx context.Context, cfg *config.Config, ip string) error {
	err := sshRun(ctx, cfg, ip, "sudo cloud-init status --wait >/dev/null 2>&1; rc=$?; cloud-init status; exit $rc")
	if err != nil {
		logf("cloud-init reported errors; last lines of /var/log/cloud-init-output.log:")
		_ = sshRun(ctx, cfg, ip, "sudo tail -n 30 /var/log/cloud-init-output.log")
	}
	return err
}
