package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetEnvOrSecret_EnvVar(t *testing.T) {
	t.Setenv("TEST_CONFIG_VAR", "from-env")
	if got := getEnvOrSecret("TEST_CONFIG_VAR"); got != "from-env" {
		t.Errorf("got %q, want %q", got, "from-env")
	}
}

func TestGetEnvOrSecret_SecretFile(t *testing.T) {
	dir := t.TempDir()
	oldDirs := secretsDirs
	secretsDirs = []string{dir}
	defer func() { secretsDirs = oldDirs }()

	// Write a secret file
	if err := os.WriteFile(filepath.Join(dir, "MY_SECRET"), []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Ensure env var is not set
	t.Setenv("MY_SECRET", "")

	if got := getEnvOrSecret("MY_SECRET"); got != "from-file" {
		t.Errorf("got %q, want %q", got, "from-file")
	}
}

func TestGetEnvOrSecret_EnvTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	oldDirs := secretsDirs
	secretsDirs = []string{dir}
	defer func() { secretsDirs = oldDirs }()

	// Write a secret file
	if err := os.WriteFile(filepath.Join(dir, "DUAL_VAR"), []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Set env var too — env should win
	t.Setenv("DUAL_VAR", "from-env")

	if got := getEnvOrSecret("DUAL_VAR"); got != "from-env" {
		t.Errorf("got %q, want %q", got, "from-env")
	}
}

func TestGetEnvOrSecret_NotFound(t *testing.T) {
	dir := t.TempDir()
	oldDirs := secretsDirs
	secretsDirs = []string{dir}
	defer func() { secretsDirs = oldDirs }()

	t.Setenv("NONEXISTENT_VAR", "")

	if got := getEnvOrSecret("NONEXISTENT_VAR"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
