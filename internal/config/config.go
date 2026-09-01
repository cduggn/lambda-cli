// Package config loads lam settings: defaults < ~/.config/lam/config < environment.
package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const Placeholder = "secret_xxx"

type Config struct {
	APIKey        string
	APIBase       string
	Region        string
	Type          string
	SSHKey        string // key name registered in Lambda
	SSHPrivateKey string // local private key path
	SSHUser       string
	ImageFamily   string
	CloudInit     string   // default template name/path, "" = none
	Exclude       []string // rsync excludes for push/pull
	TemplatesDir  string   // extra user templates dir
	ActiveTimeout time.Duration
	SSHTimeout    time.Duration
	Path          string // config file path (may not exist)
	FileExists    bool
}

// keys maps config/env variable names to setters.
var keys = map[string]func(*Config, string){
	"LAMBDA_API_KEY":      func(c *Config, v string) { c.APIKey = v },
	"LAMBDA_API_BASE":     func(c *Config, v string) { c.APIBase = v },
	"LAM_REGION":          func(c *Config, v string) { c.Region = v },
	"LAM_TYPE":            func(c *Config, v string) { c.Type = v },
	"LAM_SSH_KEY":         func(c *Config, v string) { c.SSHKey = v },
	"LAM_SSH_PRIVATE_KEY": func(c *Config, v string) { c.SSHPrivateKey = expandHome(v) },
	"LAM_SSH_USER":        func(c *Config, v string) { c.SSHUser = v },
	"LAM_IMAGE_FAMILY":    func(c *Config, v string) { c.ImageFamily = v },
	"LAM_CLOUD_INIT":      func(c *Config, v string) { c.CloudInit = v },
	"LAM_EXCLUDE":         func(c *Config, v string) { c.Exclude = splitList(v) },
	"LAM_TEMPLATES_DIR":   func(c *Config, v string) { c.TemplatesDir = expandHome(v) },
	"LAM_ACTIVE_TIMEOUT":  func(c *Config, v string) { c.ActiveTimeout = parseDur(v, c.ActiveTimeout) },
	"LAM_SSH_TIMEOUT":     func(c *Config, v string) { c.SSHTimeout = parseDur(v, c.SSHTimeout) },
}

func Defaults() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Region:        "us-east-1",
		Type:          "gpu_1x_a100_sxm4",
		SSHKey:        "inference-eng",
		SSHPrivateKey: filepath.Join(home, ".ssh", "inference-eng.pem"),
		SSHUser:       "ubuntu",
		ImageFamily:   "lambda-stack-22-04",
		Exclude:       []string{".venv", "__pycache__", ".pytest_cache", ".git", ".env", ".DS_Store"},
		TemplatesDir:  filepath.Join(home, ".config", "lam", "cloud-init"),
		ActiveTimeout: 15 * time.Minute,
		SSHTimeout:    5 * time.Minute,
		Path:          DefaultPath(),
	}
}

func DefaultPath() string {
	if p := os.Getenv("LAM_CONFIG"); p != "" {
		return expandHome(p)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "lam", "config")
}

// Load applies the config file (if present) then environment variables.
func Load() (*Config, error) {
	c := Defaults()
	if f, err := os.Open(c.Path); err == nil {
		defer f.Close()
		c.FileExists = true
		kv, err := Parse(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.Path, err)
		}
		for k, v := range kv {
			if set, ok := keys[k]; ok {
				set(c, v)
			}
		}
	}
	for k, set := range keys {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			set(c, v)
		}
	}
	return c, nil
}

func (c *Config) KeySet() bool { return c.APIKey != "" && c.APIKey != Placeholder }

// Parse reads shell-style KEY=value lines (comments, `export`, quotes tolerated).
func Parse(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, raw, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected KEY=value", n)
		}
		k = strings.TrimSpace(k)
		v := stripComment(strings.TrimSpace(raw))
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out, sc.Err()
}

// stripComment removes a trailing "# ..." comment from an unquoted value.
// A value that is entirely a comment (KEY=   # note) becomes empty, which is the
// whole point: an empty setting must stay empty. A '#' inside a value with no
// preceding whitespace (foo#bar) is kept.
func stripComment(v string) string {
	if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, `'`) {
		return v
	}
	for i := 0; i < len(v); i++ {
		if v[i] != '#' {
			continue
		}
		if i == 0 || v[i-1] == ' ' || v[i-1] == '\t' {
			return strings.TrimSpace(v[:i])
		}
	}
	return v
}

// splitList parses a comma-separated setting. An empty value clears the list.
func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func expandHome(p string) string {
	home, _ := os.UserHomeDir()
	p = strings.ReplaceAll(p, "$HOME", home)
	p = strings.ReplaceAll(p, "${HOME}", home)
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = home + p[1:]
	}
	return p
}

func parseDur(v string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	var secs int
	if _, err := fmt.Sscanf(v, "%d", &secs); err == nil {
		return time.Duration(secs) * time.Second
	}
	return fallback
}

const Example = `# lam config — sourced as KEY=value lines. Keep it 0600. Env vars override these.
LAMBDA_API_KEY=secret_xxx            # https://cloud.lambda.ai/api-keys

LAM_REGION=us-east-1
LAM_TYPE=gpu_1x_a100_sxm4            # lam types
LAM_SSH_KEY=inference-eng            # key NAME as registered in Lambda (lam keys)
LAM_SSH_PRIVATE_KEY=$HOME/.ssh/inference-eng.pem
LAM_IMAGE_FAMILY=lambda-stack-22-04  # lam images
LAM_CLOUD_INIT=                      # default template: base | vllm | /path/to/file.yaml | empty
# LAM_EXCLUDE=.venv,__pycache__,.pytest_cache,.git,.env,.DS_Store   # rsync excludes for push/pull
# LAM_TEMPLATES_DIR=$HOME/.config/lam/cloud-init   # your own NAME.yaml templates
`

// WriteExample creates the config file with the example content (0600). Fails if it exists.
func WriteExample(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(Example), 0o600)
}
