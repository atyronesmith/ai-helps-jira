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

func TestLoadTranslationNeedsProjectButNotJiraCredentials(t *testing.T) {
	dir := t.TempDir()
	oldDirs := secretsDirs
	secretsDirs = []string{dir}
	t.Cleanup(func() { secretsDirs = oldDirs })
	t.Setenv("JIRA_SERVER", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_API_TOKEN", "")
	t.Setenv("JIRA_PROJECT", "OPS")
	t.Setenv("ANTHROPIC_VERTEX_PROJECT_ID", "vertex-project")
	t.Setenv("CLOUD_ML_REGION", "us-central1")

	cfg, err := LoadTranslation("person@example.test", "")
	if err != nil {
		t.Fatalf("LoadTranslation() error = %v", err)
	}
	if cfg.JiraProject != "OPS" || cfg.JiraUser != "person@example.test" {
		t.Fatalf("translation config lost overrides: %+v", cfg)
	}
	if cfg.JiraServer != "" || cfg.JiraEmail != "" || cfg.JiraAPIToken != "" {
		t.Fatalf("expected optional Jira credentials to remain empty: %+v", cfg)
	}
	if cfg.VertexProjectID != "vertex-project" || cfg.VertexRegion != "us-central1" {
		t.Fatalf("translation config lost LLM provider fields: %+v", cfg)
	}
}

func TestLoadTranslationRequiresProjectUnlessOverridden(t *testing.T) {
	dir := t.TempDir()
	oldDirs := secretsDirs
	secretsDirs = []string{dir}
	t.Cleanup(func() { secretsDirs = oldDirs })
	t.Setenv("JIRA_SERVER", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_API_TOKEN", "")
	t.Setenv("JIRA_PROJECT", "")

	if _, err := LoadTranslation("", ""); err == nil {
		t.Fatal("LoadTranslation() succeeded without a default project")
	}
	cfg, err := LoadTranslation("", "OVERRIDE")
	if err != nil {
		t.Fatalf("LoadTranslation() with project override error = %v", err)
	}
	if cfg.JiraProject != "OVERRIDE" {
		t.Fatalf("project = %q, want OVERRIDE", cfg.JiraProject)
	}
}
