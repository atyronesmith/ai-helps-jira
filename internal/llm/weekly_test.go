package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
)

type weeklyTestProvider func(context.Context, string, string, int) (string, error)

func (f weeklyTestProvider) Complete(ctx context.Context, system, prompt string, tokens int) (string, error) {
	return f(ctx, system, prompt, tokens)
}

const validWeeklyJSON = `{"user_name":"Reviewer","projects":[{"project_name":"Example work","issue_key":"PROJ-1","bullets":["Recorded a review comment."]}]}`

func TestWeeklyStatusRejectsMalformedUnboundedAndRepetitiveReports(t *testing.T) {
	repeated := strings.Repeat("The same fragment keeps appearing without new information. ", 4)
	cases := []string{
		`{"user_name":"PRIVATE_MODEL_OUTPUT","projects":[`,
		`null`, `{}`, `{"user_name":"Reviewer","projects":[]}`,
		strings.Replace(validWeeklyJSON, `"bullets":["Recorded a review comment."]`, `"bullets":[]`, 1),
		strings.Replace(validWeeklyJSON, `"user_name":"Reviewer"`, `"user_name":" "`, 1),
		strings.Replace(validWeeklyJSON, `"projects":`, `"extra":"PRIVATE_MODEL_OUTPUT","projects":`, 1),
		validWeeklyJSON + ` {"extra":true}`,
		strings.Replace(validWeeklyJSON, `"Recorded a review comment."`, fmt.Sprintf("%q", repeated), 1),
		strings.Replace(validWeeklyJSON, `"Recorded a review comment."`, `"Recorded a review comment.","Recorded a review comment."`, 1),
		strings.Replace(validWeeklyJSON, `"Recorded a review comment."`, fmt.Sprintf("%q", strings.Repeat("x", 601)), 1),
		strings.Repeat("x", weeklyMaxBytes+1),
	}
	for i, raw := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			result, err := ParseWeeklyStatus(raw)
			if result != nil || translationerror.KindOf(err) != translationerror.InvalidOutput {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if strings.Contains(err.Error(), "PRIVATE_MODEL_OUTPUT") || len(err.Error()) > 300 {
				t.Fatalf("unsafe diagnostic: %v", err)
			}
		})
	}
}

func TestWeeklyStatusAcceptsConciseDistinctActivity(t *testing.T) {
	result, err := ParseWeeklyStatus(validWeeklyJSON)
	if err != nil || result == nil || len(result.Projects) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if weeklyRepetition("The review identified a blocker. The implementation addressed that blocker.") {
		t.Fatal("distinct activity rejected")
	}
	for _, raw := range []string{strings.Repeat("loop ", 12), strings.Repeat("one two three ", 4), strings.Repeat("a longer fragment of six separate words ", 4)} {
		if !weeklyRepetition(raw) {
			t.Errorf("missed loop %q", raw)
		}
	}
}

func TestWeeklyStatusTruncationHasSafeActionableError(t *testing.T) {
	err := weeklyProviderError(translationerror.New(translationerror.InvalidOutput, ErrIncompleteCompletion))
	if translationerror.KindOf(err) != translationerror.InvalidOutput || !strings.Contains(err.Error(), "output limit") || !strings.Contains(err.Error(), "narrow the reporting period") {
		t.Fatalf("truncation diagnostic=%v", err)
	}
}

func TestWeeklyStatusContextBoundsInitializationAndCompletion(t *testing.T) {
	items := []IssueWithComments{{Issue: jira.IssueDetail{Key: "PROJ-1"}}}
	var initializationDeadline time.Time
	factory := func(ctx context.Context, _ *config.Config) (Provider, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > weeklyGenerationTimeout {
			t.Fatal("missing generation deadline")
		}
		initializationDeadline = deadline
		return weeklyTestProvider(func(callCtx context.Context, system, prompt string, tokens int) (string, error) {
			callDeadline, _ := callCtx.Deadline()
			if callDeadline != initializationDeadline || tokens != weeklyMaxTokens {
				t.Fatal("generation did not share bounded initialization context")
			}
			if !strings.Contains(system, "Current status alone does not prove a transition") {
				t.Fatal("missing evidence constraint")
			}
			return validWeeklyJSON, nil
		}), nil
	}
	if _, err := generateWeeklyStatus(context.Background(), &config.Config{}, items, "2026-10-10", "2026-10-17", factory); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	blockingFactory := func(ctx context.Context, _ *config.Config) (Provider, error) { <-ctx.Done(); return nil, ctx.Err() }
	_, err := generateWeeklyStatus(ctx, nil, items, "", "", blockingFactory)
	if translationerror.KindOf(err) != translationerror.Timeout || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout not preserved: %v", err)
	}
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	_, err = generateWeeklyStatus(canceled, nil, items, "", "", func(context.Context, *config.Config) (Provider, error) {
		t.Fatal("initialized on canceled request")
		return nil, nil
	})
	if translationerror.KindOf(err) != translationerror.Cancellation || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not preserved: %v", err)
	}
}

func TestWeeklyStatusValidatesSourceGroupsAndDoesNotRetry(t *testing.T) {
	items := []IssueWithComments{{Issue: jira.IssueDetail{Key: "PROJ-2"}, Parent: &jira.IssueDetail{Key: "PROJ-1"}}, {Issue: jira.IssueDetail{Key: "PROJ-3"}, Parent: &jira.IssueDetail{Key: "PROJ-1"}}}
	for _, tc := range []struct {
		name, output string
		extraGroup   bool
		wantErr      bool
	}{
		{name: "one shared parent", output: validWeeklyJSON},
		{name: "invented key", output: strings.ReplaceAll(validWeeklyJSON, "PROJ-1", "PROJ-999"), wantErr: true},
		{name: "child instead of parent", output: strings.ReplaceAll(validWeeklyJSON, "PROJ-1", "PROJ-2"), wantErr: true},
		{name: "missing standalone group", output: validWeeklyJSON, extraGroup: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := append([]IssueWithComments(nil), items...)
			if tc.extraGroup {
				input = append(input, IssueWithComments{Issue: jira.IssueDetail{Key: "PROJ-4"}})
			}
			calls := 0
			factory := func(context.Context, *config.Config) (Provider, error) {
				return weeklyTestProvider(func(context.Context, string, string, int) (string, error) { calls++; return tc.output, nil }), nil
			}
			result, err := generateWeeklyStatus(context.Background(), nil, input, "", "", factory)
			if (err != nil) != tc.wantErr || calls != 1 {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
			}
			if tc.wantErr && (result != nil || translationerror.KindOf(err) != translationerror.InvalidOutput) {
				t.Fatal("invalid source groups accepted")
			}
		})
	}
}

func TestWeeklyActivityBoundsInputAndPreservesUnicodeWithoutMutatingSource(t *testing.T) {
	description := strings.Repeat("界", 200)
	items := []IssueWithComments{{Issue: jira.IssueDetail{Key: "PROJ-1", Description: description, AssigneeID: "PRIVATE_ACCOUNT_ID"}, Comments: []jira.Comment{{Body: strings.Repeat("界", 400), AuthorEmail: "PRIVATE_AUTHOR_EMAIL"}}}}
	factory := func(context.Context, *config.Config) (Provider, error) {
		return weeklyTestProvider(func(_ context.Context, _ string, prompt string, _ int) (string, error) {
			if !utf8.ValidString(prompt) || strings.Contains(prompt, "PRIVATE_ACCOUNT_ID") || strings.Contains(prompt, "PRIVATE_AUTHOR_EMAIL") {
				t.Fatal("invalid or unnecessary private fields in prompt")
			}
			var input struct {
				Items []weeklyActivity `json:"items"`
			}
			if err := json.Unmarshal([]byte(prompt), &input); err != nil {
				t.Fatal(err)
			}
			if len(input.Items) != 1 || !strings.HasSuffix(input.Items[0].Description, "...") {
				t.Fatal("missing bounded source preview")
			}
			return validWeeklyJSON, nil
		}), nil
	}
	if _, err := generateWeeklyStatus(context.Background(), nil, items, "", "", factory); err != nil {
		t.Fatal(err)
	}
	if items[0].Issue.Description != description || len(items[0].Comments[0].Body) != 1200 {
		t.Fatal("source data mutated")
	}
	items[0].Issue.Summary = strings.Repeat("x", weeklyMaxBytes+1)
	_, err := generateWeeklyStatus(context.Background(), nil, items, "", "", func(context.Context, *config.Config) (Provider, error) {
		t.Fatal("provider initialized for oversized input")
		return nil, nil
	})
	if translationerror.KindOf(err) != translationerror.Unsupported {
		t.Fatalf("oversize error=%v", err)
	}
}
