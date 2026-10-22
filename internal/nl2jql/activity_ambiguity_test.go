package nl2jql

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
)

func TestWorkedOnCannotTranslateToSingleIssueSearch(t *testing.T) {
	for _, input := range []string{
		"what jira items did I work on during Oct 12 - Oct 16",
		"What Jira items did I work on during Oct 12 - Oct 16?",
		"issues I worked on between 2026-10-12 and 2026-10-16",
		"my open issues I worked on during October 12 - October 16",
		"tickets worked on by jsmith this week",
		"what issues did jsmith work on last week",
	} {
		t.Run(input, func(t *testing.T) {
			provider := &recordingProvider{jql: supportedStatusPlan}
			tr := newRoutingTranslator(t, provider)
			result, err := tr.Translate(context.Background(), input)
			if !errors.Is(err, ErrAmbiguousIntent) || translationerror.KindOf(err) != translationerror.Unsupported {
				t.Fatalf("expected clarification, got result=%+v err=%v", result, err)
			}
			if !strings.Contains(err.Error(), "comments, logged work or status changes") {
				t.Fatalf("missing explanation of activity evidence: %v", err)
			}
			if result.Input != input || result.JQL != "" || atomic.LoadInt32(&provider.calls) != 0 {
				t.Fatalf("ambiguous activity must preserve input and avoid guessing: %+v calls=%d", result, atomic.LoadInt32(&provider.calls))
			}
		})
	}
}

func TestActivityWordsDoNotChangeLiteralOrRecommendationMeaning(t *testing.T) {
	if err := checkAmbiguity(`issues assigned to "Worked On"`); err != nil {
		t.Fatalf("quoted names must stay opaque to activity detection: %v", err)
	}
	for _, tc := range []struct{ input, want string }{
		{"my issues that contain the words worked or on", `assignee = currentUser() AND text ~ "worked OR on"`},
		{"Which of my open issues should I work on next?", `statusCategory != Done AND assignee = currentUser()`},
	} {
		t.Run(tc.input, func(t *testing.T) {
			provider := &recordingProvider{fail: errors.New("must be deterministic")}
			tr := newRoutingTranslator(t, provider)
			result, err := tr.Translate(context.Background(), tc.input)
			if err != nil || result.JQL != tc.want || atomic.LoadInt32(&provider.calls) != 0 {
				t.Fatalf("result=%+v err=%v calls=%d; want %s", result, err, atomic.LoadInt32(&provider.calls), tc.want)
			}
		})
	}
}
