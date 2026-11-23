// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package apps

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// renderTemplate mirrors the agent's substitution ($key, longest keys first).
func renderTemplate(tmpl string, env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		tmpl = strings.ReplaceAll(tmpl, "$"+k, env[k])
	}
	return tmpl
}

// sampleInput fills required values that have no default, as the dashboard form would.
func sampleInput(e AppCatalogEntry) map[string]string {
	in := map[string]string{}
	for _, v := range e.EnvVars {
		if v.Required && v.Default == "" {
			in[v.Key] = "s3cret-Value_1"
		}
	}
	return in
}

func TestCatalogTemplatesRenderCompletely(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Catalog {
		if seen[e.Name] {
			t.Fatalf("duplicate catalog name %q", e.Name)
		}
		seen[e.Name] = true

		tmpl, err := GetAppTemplate(e.Name)
		if err != nil {
			t.Fatalf("%s: %v", e.Name, err)
		}
		env, err := e.ResolveEnv(sampleInput(e))
		if err != nil {
			t.Fatalf("%s: resolve env: %v", e.Name, err)
		}
		out := renderTemplate(tmpl, env)
		if strings.Contains(out, "$") {
			t.Errorf("%s: unresolved placeholder left in compose:\n%s", e.Name, out)
		}
		var root struct {
			Services map[string]any `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(out), &root); err != nil {
			t.Errorf("%s: rendered compose is not valid YAML: %v", e.Name, err)
		}
		if len(root.Services) == 0 {
			t.Errorf("%s: rendered compose has no services", e.Name)
		}
	}
}

func TestResolveEnv(t *testing.T) {
	entry := AppCatalogEntry{EnvVars: []AppEnvVar{
		{Key: "port", Required: true, Default: "8080"},
		{Key: "password", Required: true, Secret: true},
		{Key: "key", Generate: GenerateHex32},
		{Key: "note"},
	}}

	t.Run("defaults, generation and unknown keys", func(t *testing.T) {
		got, err := entry.ResolveEnv(map[string]string{"password": "pw", "extra": "x"})
		if err != nil {
			t.Fatal(err)
		}
		if got["port"] != "8080" {
			t.Errorf("port = %q, want default 8080", got["port"])
		}
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(got["key"]) {
			t.Errorf("key = %q, want 64 hex chars", got["key"])
		}
		if v, ok := got["note"]; !ok || v != "" {
			t.Errorf("optional key must be present and empty, got %q (present=%v)", v, ok)
		}
		if _, ok := got["extra"]; ok {
			t.Error("keys outside the catalog entry must be dropped")
		}
	})

	t.Run("keeps a given value for a generated key", func(t *testing.T) {
		got, err := entry.ResolveEnv(map[string]string{"password": "pw", "key": "mine"})
		if err != nil || got["key"] != "mine" {
			t.Fatalf("got %q, %v", got["key"], err)
		}
	})

	t.Run("missing required", func(t *testing.T) {
		if _, err := entry.ResolveEnv(map[string]string{"password": "  "}); !errors.Is(err, ErrMissingAppSetting) {
			t.Fatalf("err = %v, want ErrMissingAppSetting", err)
		}
	})

	for _, bad := range []string{`a"b`, "a$b", "a`b", `a\b`, "a\nb", strings.Repeat("x", maxAppSettingLen+1)} {
		if _, err := entry.ResolveEnv(map[string]string{"password": bad}); !errors.Is(err, ErrInvalidAppSetting) {
			t.Errorf("value %q: err = %v, want ErrInvalidAppSetting", bad, err)
		}
	}
}
