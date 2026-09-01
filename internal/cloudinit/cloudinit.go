// Package cloudinit resolves and renders cloud-init user_data templates.
// Templates are looked up by name in the user templates dir, then the embedded set,
// or by path. {{VAR}} placeholders are filled from the lookup function (typically os.LookupEnv).
package cloudinit

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

//go:embed templates/*.yaml
var embedded embed.FS

const MaxBytes = 1_000_000 // Lambda's user_data cap

var placeholder = regexp.MustCompile(`\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}`)

// Names lists available template names (embedded plus userDir), sorted.
func Names(userDir string) []string {
	seen := map[string]bool{}
	entries, _ := fs.ReadDir(embedded, "templates")
	for _, e := range entries {
		seen[strings.TrimSuffix(e.Name(), ".yaml")] = true
	}
	if userDir != "" {
		if entries, err := os.ReadDir(userDir); err == nil {
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".yaml") {
					seen[strings.TrimSuffix(e.Name(), ".yaml")] = true
				}
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Source returns the raw template by name (userDir first, then embedded) or by file path.
func Source(nameOrPath, userDir string) ([]byte, string, error) {
	if b, err := os.ReadFile(nameOrPath); err == nil {
		return b, nameOrPath, nil
	}
	if userDir != "" {
		p := filepath.Join(userDir, nameOrPath+".yaml")
		if b, err := os.ReadFile(p); err == nil {
			return b, p, nil
		}
	}
	if b, err := embedded.ReadFile("templates/" + nameOrPath + ".yaml"); err == nil {
		return b, "embedded:" + nameOrPath, nil
	}
	return nil, "", fmt.Errorf("cloud-init template %q not found (embedded: %s; user dir: %s)",
		nameOrPath, strings.Join(Names(""), ", "), userDir)
}

// Render fills {{VAR}} placeholders. Missing vars render empty and are returned.
func Render(src []byte, lookup func(string) (string, bool)) (string, []string) {
	var missing []string
	seen := map[string]bool{}
	out := placeholder.ReplaceAllStringFunc(string(src), func(m string) string {
		name := m[2 : len(m)-2]
		v, ok := lookup(name)
		if !ok && !seen[name] {
			seen[name] = true
			missing = append(missing, name)
		}
		return v
	})
	sort.Strings(missing)
	return out, missing
}

// Load resolves and renders in one step.
func Load(nameOrPath, userDir string, lookup func(string) (string, bool)) (rendered string, from string, missing []string, err error) {
	src, from, err := Source(nameOrPath, userDir)
	if err != nil {
		return "", "", nil, err
	}
	rendered, missing = Render(src, lookup)
	if len(rendered) > MaxBytes {
		return "", from, missing, fmt.Errorf("rendered user_data is %d bytes; Lambda caps it at %d", len(rendered), MaxBytes)
	}
	return rendered, from, missing, nil
}
