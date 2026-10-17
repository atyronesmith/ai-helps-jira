package jira

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClientContextCancelsInFlightRequests(t *testing.T) {
	for _, operation := range []string{"read", "write", "upload"} {
		t.Run(operation, func(t *testing.T) {
			started := make(chan struct{})
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				select {
				case <-r.Context().Done():
				case <-time.After(2 * time.Second):
				}
			}))
			defer ts.Close()

			ctx, cancel := context.WithCancel(context.Background())
			client := newTestClient(ts).WithContext(ctx)
			errCh := make(chan error, 1)
			go func() {
				switch operation {
				case "read":
					errCh <- client.doRequest(http.MethodGet, "/read", nil, nil)
				case "write":
					errCh <- client.doRequest(http.MethodPost, "/write", map[string]string{"key": "value"}, nil)
				case "upload":
					path := filepath.Join(t.TempDir(), "attachment.txt")
					if err := os.WriteFile(path, []byte("attachment"), 0600); err != nil {
						errCh <- err
						return
					}
					_, err := client.AttachFile("TEST-1", path)
					errCh <- err
				}
			}()

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
		})
	}
}
