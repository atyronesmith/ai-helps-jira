package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

// secretsDirs are checked (in order) for secret files before falling back
// to environment variables. Each file should contain a single value with
// no trailing newline. File names match env var names (e.g. JIRA_API_TOKEN).
var secretsDirs = []string{"/run/secrets", "/var/run/secrets"}

// GetEnvOrSecret returns the value of an environment variable, falling back
// to reading from a secret file in /run/secrets/<name>. Exported for use by
// packages that read config directly (e.g. internal/llm).
func GetEnvOrSecret(name string) string {
	return getEnvOrSecret(name)
}

func getEnvOrSecret(name string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	for _, dir := range secretsDirs {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err == nil {
			return strings.TrimRight(string(data), "\r\n")
		}
	}
	return ""
}

type Config struct {
	JiraServer      string
	JiraEmail       string
	JiraAPIToken    string
	JiraProject     string
	JiraUser        string // optional override, empty = currentUser()
	VertexProjectID string
	VertexRegion    string
}

func Load(userOverride, projectOverride string) (*Config, error) {
	_ = godotenv.Load() // ignore error if no .env file

	required := []string{
		"JIRA_SERVER",
		"JIRA_EMAIL",
		"JIRA_API_TOKEN",
	}
	// JIRA_PROJECT is only required if no override provided
	if projectOverride == "" {
		required = append(required, "JIRA_PROJECT")
	}
	var missing []string
	for _, k := range required {
		if getEnvOrSecret(k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required config (env var or /run/secrets/ file): %s", strings.Join(missing, ", "))
	}

	project := projectOverride
	if project == "" {
		project = getEnvOrSecret("JIRA_PROJECT")
	}

	return &Config{
		JiraServer:      getEnvOrSecret("JIRA_SERVER"),
		JiraEmail:       getEnvOrSecret("JIRA_EMAIL"),
		JiraAPIToken:    getEnvOrSecret("JIRA_API_TOKEN"),
		JiraProject:     project,
		JiraUser:        userOverride,
		VertexProjectID: getEnvOrSecret("ANTHROPIC_VERTEX_PROJECT_ID"),
		VertexRegion:    getEnvOrSecret("CLOUD_ML_REGION"),
	}, nil
}

// LoadJIRAOnly loads config with only JIRA env vars required (no LLM).
func LoadJIRAOnly(userOverride, projectOverride string) (*Config, error) {
	_ = godotenv.Load()

	required := []string{
		"JIRA_SERVER",
		"JIRA_EMAIL",
		"JIRA_API_TOKEN",
	}
	if projectOverride == "" {
		required = append(required, "JIRA_PROJECT")
	}
	var missing []string
	for _, k := range required {
		if getEnvOrSecret(k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required config (env var or /run/secrets/ file): %s", strings.Join(missing, ", "))
	}

	project := projectOverride
	if project == "" {
		project = getEnvOrSecret("JIRA_PROJECT")
	}

	return &Config{
		JiraServer:      getEnvOrSecret("JIRA_SERVER"),
		JiraEmail:       getEnvOrSecret("JIRA_EMAIL"),
		JiraAPIToken:    getEnvOrSecret("JIRA_API_TOKEN"),
		JiraProject:     project,
		JiraUser:        userOverride,
		VertexProjectID: getEnvOrSecret("ANTHROPIC_VERTEX_PROJECT_ID"),
		VertexRegion:    getEnvOrSecret("CLOUD_ML_REGION"),
	}, nil
}

func (c *Config) Assignee() string {
	if c.JiraUser != "" {
		return fmt.Sprintf("%q", c.JiraUser)
	}
	return "currentUser()"
}

// AssigneeEmail returns the raw email address (or empty if using currentUser).
func (c *Config) AssigneeEmail() string {
	return c.JiraUser
}
