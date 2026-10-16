package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

func TestScopeIsolationAndCanonicalEffectiveOwner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	manager, err := OpenManager()
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	cfg := &config.Config{JiraServer: "HTTPS://JIRA.EXAMPLE.TEST/", JiraEmail: "Auth@example.test", JiraAPIToken: "secret-a", JiraProject: "APP"}
	otherOwner := *cfg
	otherOwner.JiraUser = "owner@example.test"
	otherSite := *cfg
	otherSite.JiraServer = "https://other.example.test"
	otherAuth := *cfg
	otherAuth.JiraEmail = "other@example.test"

	one, err := manager.ForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	two, err := manager.ForConfig(&otherOwner)
	if err != nil {
		t.Fatal(err)
	}
	three, err := manager.ForConfig(&otherSite)
	if err != nil {
		t.Fatal(err)
	}
	four, err := manager.ForConfig(&otherAuth)
	if err != nil {
		t.Fatal(err)
	}
	if one.scopeID != two.scopeID {
		t.Fatal("effective owner unexpectedly changed canonical site/auth scope")
	}
	if one.scopeID == three.scopeID || one.scopeID == four.scopeID {
		t.Fatal("different site or authenticated account shared scope")
	}

	detail := &jira.IssueDetail{Key: "APP-1", Summary: "scope one", Updated: time.Now().UTC()}
	if err := one.UpsertIssueDetail(detail); err != nil {
		t.Fatal(err)
	}
	if _, ok := two.GetIssueDetail("APP-1", time.Time{}); !ok {
		t.Fatal("same canonical scope could not see detail")
	}
	if _, ok := three.GetIssueDetail("APP-1", time.Time{}); ok {
		t.Fatal("different site saw cached detail")
	}
	if _, ok := four.GetIssueDetail("APP-1", time.Time{}); ok {
		t.Fatal("different authenticated account saw cached detail")
	}

	if err := one.UpsertComments([]jira.Comment{{ID: "c1", IssueKey: "APP-1", Body: "one", Created: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	comments, err := three.GetCommentsByKeys([]string{"APP-1"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 0 {
		t.Fatal("different site saw cached comments")
	}
	if err := one.UpsertProjects([]jira.Project{{Key: "APP", Name: "Scope One"}}); err != nil {
		t.Fatal(err)
	}
	projects, _, ok := three.GetProjects()
	if ok || len(projects) != 0 {
		t.Fatal("different site saw cached projects")
	}
}

func TestScopeURLNormalizationAndOldCachePathsUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyDir := filepath.Join(home, ".jira-cli")
	if err := os.MkdirAll(filepath.Join(legacyDir, "scopes"), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(legacyDir, "cache.db")
	cfg := &config.Config{JiraServer: "HTTPS://JIRA.EXAMPLE.TEST/", JiraEmail: "USER@example.test"}
	name, err := scopeName(cfg)
	if err != nil {
		t.Fatal(err)
	}
	oldScoped := filepath.Join(legacyDir, "scopes", name+".db")
	for _, path := range []string{legacy, oldScoped} {
		if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first := cfg
	second := &config.Config{JiraServer: "https://jira.example.test", JiraEmail: "user@example.test"}
	a, err := OpenForConfig(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenForConfig(second)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer b.Close()
	if a.scopeID != b.scopeID {
		t.Fatal("normalized site/account did not share scope")
	}
	if got := a.Path(); !strings.Contains(got, filepath.Join(".jira-cli", "cache-v1")) {
		t.Fatalf("new scope path=%q", got)
	}
	for _, path := range []string{legacy, oldScoped} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "preserve" {
			t.Fatalf("old path %s changed: %q %v", path, got, err)
		}
	}
}

func TestTokenlessConfigGetsIsolatedScope(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{JiraProject: "APP"}
	c, err := OpenForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.scopeID == "" {
		t.Fatal("tokenless config did not receive an isolated namespace")
	}
}
