package confluence

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientContextCancelsInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := (&Client{http: ts.Client(), base: ts.URL}).WithContext(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- client.doRequest(http.MethodGet, "/pages", nil, nil) }()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("request did not reach server")
	}
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected canceled request error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not stop after cancellation")
	}
}
