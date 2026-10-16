package jira

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type searchTransportFunc func(*http.Request) (*http.Response, error)

func (f searchTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSearchDefaultCountPolicyBoundsWorkAndRetainedRows(t *testing.T) {
	calls := 0
	c := &Client{base: "https://example.invalid", project: "TEST", http: &http.Client{Transport: searchTransportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/rest/api/3/search/jql" {
			t.Fatalf("count enumeration used unexpected endpoint %s", r.URL.Path)
		}
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 60*time.Second {
			t.Fatal("search HTTP request lacks the overall time budget")
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"maxResults":100`) {
			t.Fatalf("default page size is not 100: %s", body)
		}
		page := fmt.Sprintf(`{"issues":[{"key":"TEST-%d"}],"nextPageToken":"page-%d","isLast":false}`, calls, calls)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(page))}, nil
	})}}
	got, err := c.SearchJQLResult(context.Background(), "project = TEST", SearchOptions{ExactCount: true})
	if !errors.Is(err, ErrIncompleteSearch) || calls != 100 || got.StopReason != "page_budget" {
		t.Fatalf("count policy calls=%d result=%+v error=%v", calls, got.SearchInfo, err)
	}
	if got.Returned != 50 || len(got.Issues) != 50 || got.Enumerated != 100 || got.Total != nil || got.Complete {
		t.Fatalf("interrupted count lost preview or overstated coverage: %+v", got.SearchInfo)
	}
}

func TestSearchRespectsEarlierCallerDeadlineAndPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	calls := 0
	c := &Client{base: "https://example.invalid", project: "TEST", http: &http.Client{Transport: searchTransportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		actual, ok := r.Context().Deadline()
		if !ok || !actual.Equal(deadline) {
			t.Fatal("search extended or dropped caller's deadline")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"issues":[],"isLast":true}`))}, nil
	})}}
	got, err := c.SearchJQLResult(ctx, "project = TEST", SearchOptions{})
	if err != nil || !got.Complete || calls != 1 {
		t.Fatalf("deadline-bound empty search: result=%+v err=%v", got.SearchInfo, err)
	}
	cancel()
	got, err = c.SearchJQLResult(ctx, "project = TEST", SearchOptions{})
	if !errors.Is(err, context.Canceled) || calls != 1 || got.Complete || got.Total != nil {
		t.Fatalf("canceled search started work or claimed success: calls=%d result=%+v err=%v", calls, got.SearchInfo, err)
	}
}

type searchCancelAtEOF struct {
	io.Reader
	cancel context.CancelFunc
}

func (b searchCancelAtEOF) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		b.cancel()
	}
	return n, err
}

func TestSearchCancellationBetweenDecodedPagesPreservesRows(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	c := &Client{base: "https://example.invalid", project: "TEST", http: &http.Client{Transport: searchTransportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body := searchCancelAtEOF{
			Reader: strings.NewReader(`{"issues":[{"key":"TEST-1"}],"nextPageToken":"next"}`),
			cancel: cancel,
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body)}, nil
	})}}
	got, err := c.SearchJQLResult(ctx, "project = TEST", SearchOptions{ExactCount: true})
	if calls != 1 || !errors.Is(err, context.Canceled) || got.Returned != 1 || got.Enumerated != 1 || got.Total != nil || got.Complete {
		t.Fatalf("between-page cancellation calls=%d metadata=%+v error=%v", calls, got.SearchInfo, err)
	}
}
