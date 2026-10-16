package cache

import (
	"strings"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

func TestWeeklyCacheKeyStableAndTracksReportInputs(t *testing.T) {
	base := &config.Config{JiraServer: "https://jira.example.test/", JiraEmail: "User@example.test", JiraProject: "APP"}
	updated := time.Date(2026, 10, 1, 12, 30, 0, 0, time.FixedZone("test", 2*60*60))
	issues := []jira.Issue{
		{Key: "APP-2", Updated: updated, StatusCategory: "indeterminate", Project: "APP", Assignee: "owner@example.test", AssigneeID: "acct-2", Summary: "Second"},
		{Key: "APP-1", Updated: updated, StatusCategory: "new", Project: "APP", Assignee: "owner@example.test", AssigneeID: "acct-1", Summary: "First"},
	}
	original := append([]jira.Issue(nil), issues...)
	key := WeeklyCacheKey(base, "2026-09-28", "2026-10-02", issues)
	reordered := []jira.Issue{issues[1], issues[0]}
	if got := WeeklyCacheKey(base, "2026-09-28", "2026-10-02", reordered); got != key {
		t.Fatal("issue ordering changed the weekly key")
	}
	if issues[0] != original[0] || issues[1] != original[1] {
		t.Fatal("fingerprinting mutated the caller's issue slice")
	}

	mutations := []struct {
		name       string
		cfg        *config.Config
		start, end string
		change     func([]jira.Issue) []jira.Issue
	}{
		{name: "site", cfg: &config.Config{JiraServer: "https://other.example.test", JiraEmail: base.JiraEmail, JiraProject: base.JiraProject}},
		{name: "authenticated account", cfg: &config.Config{JiraServer: base.JiraServer, JiraEmail: "other@example.test", JiraProject: base.JiraProject}},
		{name: "effective owner", cfg: &config.Config{JiraServer: base.JiraServer, JiraEmail: base.JiraEmail, JiraProject: base.JiraProject, JiraUser: "other@example.test"}},
		{name: "project", cfg: &config.Config{JiraServer: base.JiraServer, JiraEmail: base.JiraEmail, JiraProject: "OTHER"}},
		{name: "start range", cfg: base, start: "2026-09-29"},
		{name: "end range", cfg: base, end: "2026-10-03"},
		{name: "same timestamp replacement", cfg: base, change: func(v []jira.Issue) []jira.Issue { v[0].Summary = "replacement"; return v }},
		{name: "status category", cfg: base, change: func(v []jira.Issue) []jira.Issue { v[0].StatusCategory = "done"; return v }},
		{name: "project field", cfg: base, change: func(v []jira.Issue) []jira.Issue { v[0].Project = "OTHER"; return v }},
		{name: "assignee", cfg: base, change: func(v []jira.Issue) []jira.Issue { v[0].Assignee = "other@example.test"; return v }},
		{name: "assignee id", cfg: base, change: func(v []jira.Issue) []jira.Issue { v[0].AssigneeID = "other-id"; return v }},
		{name: "summary", cfg: base, change: func(v []jira.Issue) []jira.Issue { v[0].Summary = "changed"; return v }},
		{name: "updated", cfg: base, change: func(v []jira.Issue) []jira.Issue { v[0].Updated = v[0].Updated.Add(time.Second); return v }},
		{name: "removal", cfg: base, change: func(v []jira.Issue) []jira.Issue { return v[:1] }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			if cfg == nil {
				cfg = base
			}
			start, end := "2026-09-28", "2026-10-02"
			if tc.start != "" {
				start = tc.start
			}
			if tc.end != "" {
				end = tc.end
			}
			v := append([]jira.Issue(nil), issues...)
			if tc.change != nil {
				v = tc.change(v)
			}
			if got := WeeklyCacheKey(cfg, start, end, v); got == key {
				t.Fatal("meaningful input change did not change weekly key")
			}
		})
	}
	for _, secret := range []string{base.JiraEmail, "owner@example.test", "First", "APP-1"} {
		if strings.Contains(key, secret) {
			t.Fatalf("weekly key exposed input %q", secret)
		}
	}
}
