package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadJIRAConnectionNeedsNoProjectOrProvider(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("JIRA_PROJECT", "")
	t.Setenv("LLM_PROVIDER", "not-a-provider")
	t.Setenv("JIRA_SERVER", "https://jira.example.test")
	t.Setenv("JIRA_EMAIL", "tester@example.test")
	t.Setenv("JIRA_API_TOKEN", "fixture-token")
	cfg, err := LoadJIRAConnection()
	if err != nil || cfg == nil {
		t.Fatalf("connection config required unrelated settings: %v", err)
	}
	if cfg.JiraProject != "" || cfg.JiraUser != "" || cfg.JiraServer != "https://jira.example.test" || cfg.JiraEmail != "tester@example.test" || cfg.JiraAPIToken != "fixture-token" {
		t.Fatal("connection config lost authentication settings or picked an effective-user override")
	}
}

func TestLoadJIRAConnectionUsesMountedSecretAndEnvironmentPrecedence(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := t.TempDir()
	previous := secretsDirs
	secretsDirs = []string{dir}
	t.Cleanup(func() { secretsDirs = previous })
	t.Setenv("JIRA_SERVER", "https://jira.example.test")
	t.Setenv("JIRA_EMAIL", "tester@example.test")
	t.Setenv("JIRA_API_TOKEN", "")
	if err := os.WriteFile(filepath.Join(dir, "JIRA_API_TOKEN"), []byte("mounted-fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadJIRAConnection()
	if err != nil || cfg.JiraAPIToken != "mounted-fixture" {
		t.Fatal("connection config did not use the mounted secret")
	}
	t.Setenv("JIRA_API_TOKEN", "environment-fixture")
	cfg, err = LoadJIRAConnection()
	if err != nil || cfg.JiraAPIToken != "environment-fixture" {
		t.Fatal("environment must take precedence over a mounted secret")
	}
}

func TestLoadJIRAConnectionMissingSettingsDoesNotExposeCredentials(t *testing.T) {
	t.Chdir(t.TempDir())
	previous := secretsDirs
	secretsDirs = []string{t.TempDir()}
	t.Cleanup(func() { secretsDirs = previous })
	t.Setenv("JIRA_SERVER", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_API_TOKEN", "private-fixture-marker")
	_, err := LoadJIRAConnection()
	if err == nil || !strings.Contains(err.Error(), "JIRA_SERVER, JIRA_EMAIL") || strings.Contains(err.Error(), "private-fixture-marker") {
		t.Fatal("missing connection settings were not safely reported")
	}
}
