package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

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
		if os.Getenv(k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}

	project := projectOverride
	if project == "" {
		project = os.Getenv("JIRA_PROJECT")
	}

	return &Config{
		JiraServer:      os.Getenv("JIRA_SERVER"),
		JiraEmail:       os.Getenv("JIRA_EMAIL"),
		JiraAPIToken:    os.Getenv("JIRA_API_TOKEN"),
		JiraProject:     project,
		JiraUser:        userOverride,
		VertexProjectID: os.Getenv("ANTHROPIC_VERTEX_PROJECT_ID"),
		VertexRegion:    os.Getenv("CLOUD_ML_REGION"),
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
		if os.Getenv(k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}

	project := projectOverride
	if project == "" {
		project = os.Getenv("JIRA_PROJECT")
	}

	return &Config{
		JiraServer:      os.Getenv("JIRA_SERVER"),
		JiraEmail:       os.Getenv("JIRA_EMAIL"),
		JiraAPIToken:    os.Getenv("JIRA_API_TOKEN"),
		JiraProject:     project,
		JiraUser:        userOverride,
		VertexProjectID: os.Getenv("ANTHROPIC_VERTEX_PROJECT_ID"),
		VertexRegion:    os.Getenv("CLOUD_ML_REGION"),
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
