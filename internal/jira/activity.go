package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	_ "time/tzdata" // Minimal containers need the same calendar boundaries as hosts.

	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

// WorkActivityOptions selects visible authored evidence, independently of the
// issue's current owner, status, or last update. Dates are inclusive calendar days.
type WorkActivityOptions struct {
	StartDate, EndDate, Project string
	TimeZone                    string // Empty uses the effective user's Jira profile.
	MaxIssues, MaxEventPages    int
	MaxEvents                   int // Global retained matching-evidence budget.
}

type ActivityMatch struct {
	Issue  Issue
	Events []ActivityEvent
}

// ActivityResult describes enumeration, not an atomic historical snapshot.
// Complete covers only currently visible records in the selected current project.
type ActivityResult struct {
	Project, AccountID, TimeZone, StartDate, EndDate, CandidateJQL string
	TimeZoneAssumed, CandidateExecuted                             bool
	Matches                                                        []ActivityMatch
	Candidates, Scanned                                            int
	CandidateComplete, Complete                                    bool
	Problems                                                       []string
}

// WorkActivity retrieves comments, worklogs, and status history by exact actor
// account ID and event time. The project-wide candidate scan deliberately has no
// ownership or updated-time filter: backdated worklogs and later changes must not
// hide evidence. Budgets and failed pages always leave the report incomplete.
func (c *Client) WorkActivity(ctx context.Context, opts WorkActivityOptions) (ActivityResult, error) {
	r := ActivityResult{Project: opts.Project, StartDate: opts.StartDate, EndDate: opts.EndDate}
	if r.Project == "" {
		r.Project = c.project
	}
	if strings.TrimSpace(r.Project) == "" {
		return r, errors.New("activity requires a project")
	}
	if opts.MaxIssues == 0 {
		opts.MaxIssues = 1000
	}
	if opts.MaxEventPages == 0 {
		opts.MaxEventPages = 20
	}
	if opts.MaxEvents == 0 {
		opts.MaxEvents = 10000
	}
	if opts.MaxIssues < 1 || opts.MaxIssues > 10000 || opts.MaxEventPages < 1 || opts.MaxEventPages > 100 || opts.MaxEvents < 1 || opts.MaxEvents > 50000 {
		return r, errors.New("activity budgets must be 1..10000 issues, 1..100 event pages per endpoint, and 1..50000 retained events")
	}
	// Validate calendar syntax before performing any remote calls.
	if _, _, err := activityBounds(opts.StartDate, opts.EndDate, time.UTC); err != nil {
		return r, err
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	profile, err := c.activityProfile(ctx)
	if err != nil {
		return r, err
	}
	r.AccountID = profile.AccountID
	r.TimeZone = opts.TimeZone
	if r.TimeZone == "" {
		r.TimeZone = profile.TimeZone
	}
	if r.TimeZone == "" {
		r.TimeZone, r.TimeZoneAssumed = "UTC", true
	}
	loc, err := time.LoadLocation(r.TimeZone)
	if err != nil {
		return r, fmt.Errorf("activity timezone %q is not a recognized IANA timezone", r.TimeZone)
	}
	start, until, err := activityBounds(opts.StartDate, opts.EndDate, loc)
	if err != nil {
		return r, err
	}
	r.CandidateJQL = (&jql.Query{
		Where:   jql.Compare("project", "=", jql.QuoteIdent(r.Project)),
		OrderBy: []jql.Sort{{Field: "key", Direction: "ASC"}},
	}).Render()
	if err := jql.Validate(r.CandidateJQL, jql.Options{DefaultProject: r.Project}); err != nil {
		return r, err
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	r.CandidateExecuted = true
	candidates, searchErr := c.SearchJQLResult(ctx, r.CandidateJQL, SearchOptions{
		Limit: opts.MaxIssues, PageSize: 100, MaxPages: 100,
	})
	r.Candidates, r.CandidateComplete = len(candidates.Issues), candidates.Complete
	if searchErr != nil {
		r.Problems = append(r.Problems, "candidate search failed; matching total unknown")
	} else if !candidates.Complete {
		r.Problems = append(r.Problems, "candidate search incomplete ("+candidates.StopReason+"); matching total unknown")
	}

	// Each worker owns one result slot; merge only after all workers finish. A
	// bounded pool makes large projects usable without unbounded API fan-out.
	type evidence struct {
		events []ActivityEvent
		err    error
	}
	rows := make([]evidence, len(candidates.Issues))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var retained atomic.Int64
	var evidenceLimit atomic.Bool
	eventLimitErr := errors.New("activity matching-evidence budget reached")
	for worker := 0; worker < min(4, len(rows)); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				issue := candidates.Issues[i]
				if evidenceLimit.Load() {
					rows[i].err = eventLimitErr
					continue
				}
				if !strings.EqualFold(issue.Project, r.Project) {
					rows[i].err = errors.New("candidate is missing or outside the selected project")
					continue
				}
				events, readErr := c.ReadActivityEvents(ctx, issue.Key, opts.MaxEventPages)
				rows[i].err = readErr
				for _, e := range events {
					if e.AuthorID == r.AccountID && !e.At.Before(start) && e.At.Before(until) {
						if retained.Add(1) > int64(opts.MaxEvents) {
							evidenceLimit.Store(true)
							rows[i].err = errors.Join(rows[i].err, eventLimitErr)
							break
						}
						rows[i].events = append(rows[i].events, e)
					}
				}
			}
		}()
	}
	for i := range rows {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if evidenceLimit.Load() {
		r.Problems = append(r.Problems, fmt.Sprintf("global matching-evidence limit of %d events reached; scan stopped and matching total unknown", opts.MaxEvents))
	}
	for i, row := range rows {
		issue := candidates.Issues[i]
		if row.err != nil {
			// Error payloads may contain private upstream data. Store a bounded
			// diagnosis and issue key; never retain raw HTTP response bodies.
			r.Problems = append(r.Problems, fmt.Sprintf("%s: activity records incomplete", issue.Key))
		} else {
			r.Scanned++
		}
		matching := row.events
		if len(matching) != 0 {
			sort.Slice(matching, func(i, j int) bool {
				if !matching[i].At.Equal(matching[j].At) {
					return matching[i].At.Before(matching[j].At)
				}
				return matching[i].Kind+":"+matching[i].ID < matching[j].Kind+":"+matching[j].ID
			})
			r.Matches = append(r.Matches, ActivityMatch{Issue: issue, Events: matching})
		}
	}
	sort.Slice(r.Matches, func(i, j int) bool { return r.Matches[i].Issue.Key < r.Matches[j].Issue.Key })
	r.Complete = r.CandidateComplete && len(r.Problems) == 0 && ctx.Err() == nil
	if !r.Complete {
		if ctx.Err() != nil {
			r.Problems = append(r.Problems, "activity deadline or cancellation interrupted the scan")
		}
		return r, fmt.Errorf("activity scan incomplete: %w", errors.Join(ErrIncompleteSearch, searchErr, ctx.Err()))
	}
	return r, nil
}

func activityBounds(startDate, endDate string, loc *time.Location) (time.Time, time.Time, error) {
	start, e1 := time.ParseInLocation("2006-01-02", startDate, loc)
	end, e2 := time.ParseInLocation("2006-01-02", endDate, loc)
	if e1 != nil || e2 != nil || start.Format("2006-01-02") != startDate || end.Format("2006-01-02") != endDate || start.Year() < 1 || end.Year() > 9999 || start.After(end) || end.AddDate(0, 0, 1).Year() > 9999 {
		return time.Time{}, time.Time{}, errors.New("activity requires ordered valid YYYY-MM-DD calendar dates with a representable following day")
	}
	until := end.AddDate(0, 0, 1)
	if !until.After(end) {
		return time.Time{}, time.Time{}, errors.New("activity end date has no unambiguous following midnight in the selected timezone")
	}
	return start, until, nil
}

type activityUser struct {
	AccountID    string `json:"accountId"`
	EmailAddress string `json:"emailAddress"`
	TimeZone     string `json:"timeZone"`
}

func (c *Client) activityProfile(ctx context.Context) (activityUser, error) {
	var me activityUser
	if err := c.doRequestContext(ctx, http.MethodGet, "/rest/api/3/myself", nil, &me); err != nil {
		return me, fmt.Errorf("resolve activity account: %w", err)
	}
	if me.AccountID == "" {
		return me, errors.New("Jira /myself response omitted the account ID")
	}
	if c.userOverride == "" || c.userOverride == me.AccountID || (me.EmailAddress != "" && strings.EqualFold(c.userOverride, me.EmailAddress)) || strings.EqualFold(c.userOverride, c.authEmail) {
		return me, nil
	}
	if !strings.Contains(c.userOverride, "@") {
		var u activityUser
		path := "/rest/api/3/user?accountId=" + url.QueryEscape(c.userOverride)
		if err := c.doRequestContext(ctx, http.MethodGet, path, nil, &u); err != nil {
			return u, fmt.Errorf("resolve activity user override by account ID (use an account ID or visible email): %w", err)
		}
		if u.AccountID != c.userOverride {
			return u, errors.New("Jira returned a different account ID for the activity user override")
		}
		return u, nil
	}
	var users []activityUser
	path := "/rest/api/3/user/search?query=" + url.QueryEscape(c.userOverride) + "&maxResults=1000"
	if err := c.doRequestContext(ctx, http.MethodGet, path, nil, &users); err != nil {
		return activityUser{}, fmt.Errorf("resolve activity user override by exact email: %w", err)
	}
	matches := make(map[string]activityUser)
	for _, u := range users {
		if u.AccountID != "" && u.EmailAddress != "" && strings.EqualFold(u.EmailAddress, c.userOverride) {
			matches[u.AccountID] = u
		}
	}
	if len(matches) != 1 || len(users) >= 1000 {
		return activityUser{}, errors.New("activity user override could not be resolved uniquely by exact email; supply an account ID")
	}
	for _, u := range matches {
		return u, nil
	}
	panic("unreachable")
}
