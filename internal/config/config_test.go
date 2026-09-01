package config

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	in := `# comment
LAMBDA_API_KEY=secret_abc   # trailing comment
export LAM_REGION="us-west-1"
LAM_TYPE='gpu_1x_h100_pcie'
LAM_CLOUD_INIT=

`
	kv, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"LAMBDA_API_KEY": "secret_abc", "LAM_REGION": "us-west-1", "LAM_TYPE": "gpu_1x_h100_pcie", "LAM_CLOUD_INIT": ""}
	for k, v := range want {
		if kv[k] != v {
			t.Errorf("%s = %q, want %q", k, kv[k], v)
		}
	}
	if _, err := Parse(strings.NewReader("garbage line")); err == nil {
		t.Error("expected error on malformed line")
	}
}

// The shipped example file must round-trip: every documented value has a trailing
// comment, and LAM_CLOUD_INIT is deliberately empty.
func TestParseExampleFile(t *testing.T) {
	kv, err := Parse(strings.NewReader(Example))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"LAMBDA_API_KEY":      Placeholder,
		"LAM_REGION":          "us-east-1",
		"LAM_TYPE":            "gpu_1x_a100_sxm4",
		"LAM_SSH_KEY":         "inference-eng",
		"LAM_SSH_PRIVATE_KEY": "$HOME/.ssh/inference-eng.pem",
		"LAM_IMAGE_FAMILY":    "lambda-stack-22-04",
		"LAM_CLOUD_INIT":      "",
	}
	for k, v := range want {
		if got, ok := kv[k]; !ok || got != v {
			t.Errorf("%s = %q (present=%v), want %q", k, got, ok, v)
		}
	}
}

func TestStripComment(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"# only a comment":          "",
		"#nospace":                  "",
		"value   # trailing":        "value",
		"value\t# tabbed":           "value",
		"foo#bar":                   "foo#bar",
		"gpu_1x_a100_sxm4":          "gpu_1x_a100_sxm4",
		`"quoted # kept"`:           `"quoted # kept"`,
		"secret_xxx  # https://x/y": "secret_xxx",
	}
	for in, want := range cases {
		if got := stripComment(in); got != want {
			t.Errorf("stripComment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config"
	if err := WriteExample(path); err != nil {
		t.Fatal(err)
	}
	if err := WriteExample(path); err == nil {
		t.Error("WriteExample should refuse to overwrite")
	}
	t.Setenv("LAM_CONFIG", path)
	t.Setenv("LAM_REGION", "eu-central-1")
	t.Setenv("LAMBDA_API_KEY", "")
	t.Setenv("LAM_ACTIVE_TIMEOUT", "90")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.FileExists || c.Region != "eu-central-1" || c.Type != "gpu_1x_a100_sxm4" {
		t.Errorf("env should override file, file should override defaults: %+v", c)
	}
	if c.KeySet() {
		t.Error("placeholder key must count as unset")
	}
	if c.CloudInit != "" {
		t.Errorf("CloudInit = %q, want empty (the example file leaves it unset)", c.CloudInit)
	}
	if c.ActiveTimeout.Seconds() != 90 {
		t.Errorf("ActiveTimeout = %s", c.ActiveTimeout)
	}
	if strings.Contains(c.SSHPrivateKey, "$HOME") {
		t.Errorf("$HOME not expanded: %s", c.SSHPrivateKey)
	}
}
