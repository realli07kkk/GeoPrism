package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureConfigDirReadyMigratesLegacyConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	legacyDir := filepath.Join(home, "Library", "Application Support", legacyAppDirName, "config")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	legacyContent := []byte(`{"legacy":"yes"}`)
	legacyPath := filepath.Join(legacyDir, "providers.json")
	if err := os.WriteFile(legacyPath, legacyContent, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	configDir, err := ensureConfigDirReady()
	if err != nil {
		t.Fatalf("ensureConfigDirReady() error = %v", err)
	}

	newPath := filepath.Join(configDir, "providers.json")
	got, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != string(legacyContent) {
		t.Fatalf("new config content = %q, want %q", string(got), string(legacyContent))
	}

	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy config should still exist, err = %v", err)
	}
}

func TestEnsureConfigDirReadyDoesNotOverwriteNewConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	newDir := filepath.Join(home, ".geoprism", "config")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	newPath := filepath.Join(newDir, "providers.json")
	if err := os.WriteFile(newPath, []byte(`{"new":"yes"}`), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	legacyDir := filepath.Join(home, "Library", "Application Support", legacyAppDirName, "config")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "providers.json"), []byte(`{"legacy":"yes"}`), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := ensureConfigDirReady(); err != nil {
		t.Fatalf("ensureConfigDirReady() error = %v", err)
	}

	got, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != `{"new":"yes"}` {
		t.Fatalf("new config content = %q, want original", string(got))
	}
}
