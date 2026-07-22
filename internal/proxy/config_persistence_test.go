package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveConfigPathUsesAccountsVolumeWhenConfigured(t *testing.T) {
	accountsDir := filepath.Join(t.TempDir(), "accounts")
	defaultPath := filepath.Join(t.TempDir(), "config.yaml")
	defaultData := []byte("proxy:\n  use_notion_personal_instructions: true\n")
	if err := os.WriteFile(defaultPath, defaultData, 0o644); err != nil {
		t.Fatalf("write default config: %v", err)
	}
	t.Setenv("CONFIG_PATH", "")
	t.Setenv("ACCOUNTS_DIR", accountsDir)

	got, err := ResolveConfigPath(defaultPath)
	if err != nil {
		t.Fatalf("ResolveConfigPath: %v", err)
	}
	want := filepath.Join(accountsDir, ".notion-manager-config.yaml")
	if got != want {
		t.Fatalf("path=%q, want %q", got, want)
	}
	persisted, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read seeded config: %v", err)
	}
	if string(persisted) != string(defaultData) {
		t.Fatalf("seeded config changed: %q", persisted)
	}

	if err := os.WriteFile(want, []byte("proxy:\n  use_notion_personal_instructions: false\n"), 0o600); err != nil {
		t.Fatalf("update persistent config: %v", err)
	}
	got, err = ResolveConfigPath(defaultPath)
	if err != nil {
		t.Fatalf("ResolveConfigPath existing: %v", err)
	}
	if got != want {
		t.Fatalf("existing path=%q, want %q", got, want)
	}
	persisted, _ = os.ReadFile(want)
	if !strings.Contains(string(persisted), "use_notion_personal_instructions: false") {
		t.Fatalf("existing persistent settings were overwritten: %s", persisted)
	}
}

func TestResolveConfigPathExplicitOverride(t *testing.T) {
	defaultPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(defaultPath, []byte("server:\n  port: 9000\n"), 0o644); err != nil {
		t.Fatalf("write default config: %v", err)
	}
	explicit := filepath.Join(t.TempDir(), "persistent", "settings.yaml")
	t.Setenv("CONFIG_PATH", explicit)
	t.Setenv("ACCOUNTS_DIR", filepath.Join(t.TempDir(), "ignored-accounts"))

	got, err := ResolveConfigPath(defaultPath)
	if err != nil {
		t.Fatalf("ResolveConfigPath: %v", err)
	}
	if got != explicit {
		t.Fatalf("path=%q, want explicit %q", got, explicit)
	}
	if _, err := os.Stat(explicit); err != nil {
		t.Fatalf("explicit config was not seeded: %v", err)
	}
}

func TestResolveConfigPathKeepsLocalDefaultWithoutVolumeEnv(t *testing.T) {
	defaultPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("CONFIG_PATH", "")
	t.Setenv("ACCOUNTS_DIR", "")
	got, err := ResolveConfigPath(defaultPath)
	if err != nil {
		t.Fatalf("ResolveConfigPath: %v", err)
	}
	if got != defaultPath {
		t.Fatalf("path=%q, want local default %q", got, defaultPath)
	}
}
