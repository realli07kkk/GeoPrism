package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureConfigDirReadyCreatesConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir, err := ensureConfigDirReady()
	if err != nil {
		t.Fatalf("ensureConfigDirReady() error = %v", err)
	}

	wantDir := filepath.Join(home, ".geoprism", "config")
	if configDir != wantDir {
		t.Fatalf("configDir = %q, want %q", configDir, wantDir)
	}
	if info, err := os.Stat(configDir); err != nil {
		t.Fatalf("Stat() error = %v", err)
	} else if !info.IsDir() {
		t.Fatalf("configDir should be directory")
	}
}

func TestEnsureConfigDirReadyDoesNotCreateProviderFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir, err := ensureConfigDirReady()
	if err != nil {
		t.Fatalf("ensureConfigDirReady() error = %v", err)
	}

	for _, name := range []string{"providers.json", "providers.toml"} {
		if _, err := os.Stat(filepath.Join(configDir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s should not be created by ensureConfigDirReady, err = %v", name, err)
		}
	}
}
