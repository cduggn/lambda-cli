package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/cduggn/lambda-cli/internal/config"
)

func testCfg() *config.Config {
	return &config.Config{SSHUser: "ubuntu", SSHPrivateKey: "/keys/id.pem", Exclude: []string{".venv", ".git"}}
}

func TestRsyncArgs(t *testing.T) {
	cfg := testCfg()
	got := rsyncArgs(cfg, syncOpts{excludes: cfg.Exclude}, "./src/", "ubuntu@1.2.3.4:~/dst/")
	want := []string{
		"-avz", "--progress",
		"--exclude", ".venv", "--exclude", ".git",
		"-e", "ssh -i /keys/id.pem -o StrictHostKeyChecking=accept-new",
		"./src/", "ubuntu@1.2.3.4:~/dst/",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestRsyncArgsFlagsAndPassthroughOrder(t *testing.T) {
	got := rsyncArgs(testCfg(), syncOpts{dryRun: true, delete: true, extra: []string{"--bwlimit=1000"}}, "a", "b")
	joined := strings.Join(got, " ")
	for _, want := range []string{"-n", "--delete", "--bwlimit=1000"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %q", want, joined)
		}
	}
	// Source and destination must stay last, after any passthrough args.
	if got[len(got)-2] != "a" || got[len(got)-1] != "b" {
		t.Errorf("src/dst not last: %q", got)
	}
	// Passthrough must come after -e so it cannot be swallowed as its value.
	if idxOf(got, "--bwlimit=1000") <= idxOf(got, "-e")+1 {
		t.Errorf("passthrough placed before the transport value: %q", got)
	}
}

func idxOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

func TestRemoteSpec(t *testing.T) {
	if got := remoteSpec(testCfg(), "1.2.3.4", "~/class7/"); got != "ubuntu@1.2.3.4:~/class7/" {
		t.Errorf("remoteSpec = %q", got)
	}
}

func TestDefaultRemoteDir(t *testing.T) {
	cwd := defaultRemoteDir(".")
	if !strings.HasPrefix(cwd, "~/") || !strings.HasSuffix(cwd, "/") {
		t.Errorf("defaultRemoteDir(\".\") = %q", cwd)
	}
	cases := map[string]string{
		filepath.FromSlash("/a/b/class7"):  "~/class7/",
		filepath.FromSlash("/a/b/class7/"): "~/class7/",
		filepath.FromSlash("/"):            "~/",
	}
	for in, want := range cases {
		if got := defaultRemoteDir(in); got != want {
			t.Errorf("defaultRemoteDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExcludesDefaultToConfigAndAreReplaceableByFlag(t *testing.T) {
	cmd := newPush(&App{})
	cfg := testCfg()
	if got := pickExcludes(cfg, cmd, nil); len(got) != 2 {
		t.Errorf("unset flag should use the configured list, got %v", got)
	}
	if err := cmd.Flags().Set("exclude", "onlythis"); err != nil {
		t.Fatal(err)
	}
	got := pickExcludes(cfg, cmd, []string{"onlythis"})
	if len(got) != 1 || got[0] != "onlythis" {
		t.Errorf("flag should replace the configured list, got %v", got)
	}
}

func TestDisplayArgsQuotesTheTransport(t *testing.T) {
	got := displayArgs(rsyncArgs(testCfg(), syncOpts{}, "a", "b"))
	if !strings.Contains(got, "-e 'ssh -i /keys/id.pem -o StrictHostKeyChecking=accept-new'") {
		t.Errorf("transport not quoted for display: %s", got)
	}
	if strings.Contains(displayArgs([]string{"-avz", "a"}), "'") {
		t.Error("plain args should not be quoted")
	}
}
