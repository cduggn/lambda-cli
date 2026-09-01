package cloudinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedTemplatesRender(t *testing.T) {
	names := Names("")
	if len(names) < 2 || names[0] != "base" || names[1] != "vllm" {
		t.Fatalf("embedded names = %v", names)
	}
	env := map[string]string{"HF_TOKEN": "hf_abc", "MODEL": "Qwen/Qwen3-0.6B"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	out, from, missing, err := Load("vllm", "", lookup)
	if err != nil || from != "embedded:vllm" || len(missing) != 0 {
		t.Fatalf("from=%s missing=%v err=%v", from, missing, err)
	}
	if !strings.HasPrefix(out, "#cloud-config") || !strings.Contains(out, `HF_TOKEN="hf_abc"`) || strings.Contains(out, "{{") {
		t.Errorf("render:\n%s", out)
	}
	_, _, missing, _ = Load("vllm", "", func(string) (string, bool) { return "", false })
	if strings.Join(missing, ",") != "HF_TOKEN,MODEL" {
		t.Errorf("missing = %v", missing)
	}
}

func TestUserDirAndPathOverrideEmbedded(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "base.yaml"), []byte("#cloud-config\nruncmd: [echo {{X}}]\n"), 0o644)
	out, from, _, err := Load("base", dir, func(string) (string, bool) { return "y", true })
	if err != nil || from != filepath.Join(dir, "base.yaml") || !strings.Contains(out, "echo y") {
		t.Fatalf("from=%s out=%q err=%v", from, out, err)
	}
	if _, _, _, err := Load("nope", dir, nil); err == nil {
		t.Error("expected not-found error")
	}
	big := filepath.Join(dir, "big.yaml")
	os.WriteFile(big, []byte(strings.Repeat("x", MaxBytes+1)), 0o644)
	if _, _, _, err := Load(big, "", nil); err == nil {
		t.Error("expected size error")
	}
}
