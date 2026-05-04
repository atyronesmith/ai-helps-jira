package confluence

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestClient creates a Client pointed at a test server.
// The test server URL replaces the base (which normally ends in /wiki).
func newTestClient(ts *httptest.Server) *Client {
	return &Client{
		http:  ts.Client(),
		base:  ts.URL,
		email: "test@example.com",
		token: "test-token",
	}
}

// jsonResponse writes a JSON response to w.
func jsonResponse(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// readBody reads and unmarshals the request body.
func readBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return m
}

func TestCreatePage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v2/pages" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}

		payload := readBody(t, r)
		if payload["spaceId"] != "12345" {
			t.Errorf("expected spaceId 12345, got %v", payload["spaceId"])
		}
		if payload["parentId"] != "parent1" {
			t.Errorf("expected parentId parent1, got %v", payload["parentId"])
		}
		if payload["title"] != "Test Page" {
			t.Errorf("expected title 'Test Page', got %v", payload["title"])
		}

		jsonResponse(t, w, 200, map[string]any{
			"id":      "99999",
			"title":   "Test Page",
			"spaceId": "12345",
			"version": map[string]any{"number": 1},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	page, err := c.CreatePage("12345", "parent1", "Test Page", "<p>Hello</p>")
	if err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	if page.ID != "99999" {
		t.Errorf("expected ID 99999, got %s", page.ID)
	}
	if page.Title != "Test Page" {
		t.Errorf("expected title 'Test Page', got %s", page.Title)
	}
	if page.SpaceID != "12345" {
		t.Errorf("expected spaceId 12345, got %s", page.SpaceID)
	}
}

func TestUpdatePage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/api/v2/pages/99999" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}

		payload := readBody(t, r)
		if payload["id"] != "99999" {
			t.Errorf("expected id 99999, got %v", payload["id"])
		}
		version := payload["version"].(map[string]any)
		if version["number"] != float64(3) {
			t.Errorf("expected version 3, got %v", version["number"])
		}

		w.WriteHeader(200)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.UpdatePage("99999", "Updated Title", "<p>Updated</p>", 2)
	if err != nil {
		t.Fatalf("UpdatePage: %v", err)
	}
}

func TestGetPageBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v2/pages/99999" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("body-format") != "storage" {
			t.Errorf("expected body-format=storage, got %s", r.URL.Query().Get("body-format"))
		}

		jsonResponse(t, w, 200, map[string]any{
			"id":      "99999",
			"title":   "My Page",
			"spaceId": "12345",
			"version": map[string]any{"number": 5},
			"body": map[string]any{
				"storage": map[string]any{
					"value": "<p>Page content</p>",
				},
			},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	page, body, err := c.GetPageBody("99999")
	if err != nil {
		t.Fatalf("GetPageBody: %v", err)
	}
	if page.ID != "99999" {
		t.Errorf("expected ID 99999, got %s", page.ID)
	}
	if page.Version.Number != 5 {
		t.Errorf("expected version 5, got %d", page.Version.Number)
	}
	if body != "<p>Page content</p>" {
		t.Errorf("expected body '<p>Page content</p>', got %q", body)
	}
}

func TestCreateBlogPost(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v2/blogposts" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}

		payload := readBody(t, r)
		if payload["spaceId"] != "12345" {
			t.Errorf("expected spaceId 12345, got %v", payload["spaceId"])
		}
		if payload["title"] != "My Blog Post" {
			t.Errorf("expected title 'My Blog Post', got %v", payload["title"])
		}

		jsonResponse(t, w, 200, map[string]any{
			"id":      "88888",
			"title":   "My Blog Post",
			"spaceId": "12345",
			"version": map[string]any{"number": 1},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	post, err := c.CreateBlogPost("12345", "My Blog Post", "<p>Blog content</p>")
	if err != nil {
		t.Fatalf("CreateBlogPost: %v", err)
	}
	if post.ID != "88888" {
		t.Errorf("expected ID 88888, got %s", post.ID)
	}
	if post.Title != "My Blog Post" {
		t.Errorf("expected title 'My Blog Post', got %s", post.Title)
	}
}

func TestUpdateBlogPost(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/api/v2/blogposts/88888" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}

		payload := readBody(t, r)
		if payload["id"] != "88888" {
			t.Errorf("expected id 88888, got %v", payload["id"])
		}
		version := payload["version"].(map[string]any)
		if version["number"] != float64(2) {
			t.Errorf("expected version 2, got %v", version["number"])
		}

		w.WriteHeader(200)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.UpdateBlogPost("88888", "Updated Blog", "<p>Updated content</p>", 1)
	if err != nil {
		t.Fatalf("UpdateBlogPost: %v", err)
	}
}

func TestGetBlogPostBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v2/blogposts/88888" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("body-format") != "storage" {
			t.Errorf("expected body-format=storage")
		}

		jsonResponse(t, w, 200, map[string]any{
			"id":      "88888",
			"title":   "My Blog Post",
			"spaceId": "12345",
			"version": map[string]any{"number": 3},
			"body": map[string]any{
				"storage": map[string]any{
					"value": "<p>Blog body</p>",
				},
			},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	post, body, err := c.GetBlogPostBody("88888")
	if err != nil {
		t.Fatalf("GetBlogPostBody: %v", err)
	}
	if post.ID != "88888" {
		t.Errorf("expected ID 88888, got %s", post.ID)
	}
	if post.Version.Number != 3 {
		t.Errorf("expected version 3, got %d", post.Version.Number)
	}
	if body != "<p>Blog body</p>" {
		t.Errorf("expected body '<p>Blog body</p>', got %q", body)
	}
}

func TestGetChildPages(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v2/pages/11111/children" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}

		jsonResponse(t, w, 200, map[string]any{
			"results": []map[string]any{
				{"id": "22222", "title": "Child A", "spaceId": "12345"},
				{"id": "33333", "title": "Child B", "spaceId": "12345"},
			},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	children, err := c.GetChildPages("11111")
	if err != nil {
		t.Fatalf("GetChildPages: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
	if children[0].Title != "Child A" {
		t.Errorf("expected 'Child A', got %s", children[0].Title)
	}
	if children[1].ID != "33333" {
		t.Errorf("expected ID 33333, got %s", children[1].ID)
	}
}

func TestGetPageViews(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/analytics/content/99999/views" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		jsonResponse(t, w, 200, map[string]any{"count": 42})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	views, err := c.GetPageViews("99999")
	if err != nil {
		t.Fatalf("GetPageViews: %v", err)
	}
	if views != 42 {
		t.Errorf("expected 42 views, got %d", views)
	}
}

func TestGetPageViewers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/analytics/content/99999/viewers" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		jsonResponse(t, w, 200, map[string]any{"count": 7})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	viewers, err := c.GetPageViewers("99999")
	if err != nil {
		t.Fatalf("GetPageViewers: %v", err)
	}
	if viewers != 7 {
		t.Errorf("expected 7 viewers, got %d", viewers)
	}
}

func TestSearchCQL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/content/search" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}

		cql := r.URL.Query().Get("cql")
		if cql == "" {
			t.Error("expected cql query parameter")
		}

		jsonResponse(t, w, 200, map[string]any{
			"results": []map[string]any{
				{
					"id":      "44444",
					"title":   "Found Page",
					"space":   map[string]any{"key": "ENG"},
					"version": map[string]any{"number": 2},
				},
			},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	pages, err := c.SearchCQL("title = \"Found Page\"", 10)
	if err != nil {
		t.Fatalf("SearchCQL: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("expected 1 result, got %d", len(pages))
	}
	if pages[0].ID != "44444" {
		t.Errorf("expected ID 44444, got %s", pages[0].ID)
	}
	if pages[0].SpaceKey != "ENG" {
		t.Errorf("expected space key ENG, got %s", pages[0].SpaceKey)
	}
}

func TestGetPageComments(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/content/99999/child/comment" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}

		jsonResponse(t, w, 200, map[string]any{
			"results": []map[string]any{
				{
					"id":    "c1",
					"title": "Re: Page",
					"body": map[string]any{
						"view": map[string]any{
							"value": "Great page!",
						},
					},
					"version": map[string]any{
						"by": map[string]any{
							"displayName": "Alice",
						},
					},
				},
				{
					"id":    "c2",
					"title": "Re: Page",
					"body": map[string]any{
						"view": map[string]any{
							"value": "Thanks!",
						},
					},
					"version": map[string]any{
						"by": map[string]any{
							"displayName": "Bob",
						},
					},
				},
			},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	comments, err := c.GetPageComments("99999")
	if err != nil {
		t.Fatalf("GetPageComments: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}
	if comments[0].Author != "Alice" {
		t.Errorf("expected author Alice, got %s", comments[0].Author)
	}
	if comments[0].Body != "Great page!" {
		t.Errorf("expected body 'Great page!', got %s", comments[0].Body)
	}
	if comments[1].Author != "Bob" {
		t.Errorf("expected author Bob, got %s", comments[1].Author)
	}
}

func TestAddLabel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/rest/api/content/99999/label" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}

		body, _ := io.ReadAll(r.Body)
		var payload []map[string]string
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal label payload: %v", err)
		}
		if len(payload) != 1 || payload[0]["name"] != "weekly-status" {
			t.Errorf("unexpected label payload: %v", payload)
		}

		w.WriteHeader(200)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.AddLabel("99999", "weekly-status")
	if err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
}

func TestSearchPagesByTitle(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/pages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}

		title := r.URL.Query().Get("title")
		if title != "Weekly Status" {
			t.Errorf("expected title 'Weekly Status', got %q", title)
		}

		jsonResponse(t, w, 200, map[string]any{
			"results": []map[string]any{
				{"id": "55555", "title": "Weekly Status", "spaceId": "12345"},
			},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	pages, err := c.SearchPagesByTitle("Weekly Status")
	if err != nil {
		t.Fatalf("SearchPagesByTitle: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("expected 1 result, got %d", len(pages))
	}
	if pages[0].ID != "55555" {
		t.Errorf("expected ID 55555, got %s", pages[0].ID)
	}
}

func TestSearchBlogPostsByTitle(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/content/search" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}

		jsonResponse(t, w, 200, map[string]any{
			"results": []map[string]any{
				{
					"id":      "88888",
					"title":   "My Blog",
					"version": map[string]any{"number": 2},
				},
			},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	posts, err := c.SearchBlogPostsByTitle("My Blog")
	if err != nil {
		t.Fatalf("SearchBlogPostsByTitle: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("expected 1 result, got %d", len(posts))
	}
	if posts[0].ID != "88888" {
		t.Errorf("expected ID 88888, got %s", posts[0].ID)
	}
	if posts[0].Version.Number != 2 {
		t.Errorf("expected version 2, got %d", posts[0].Version.Number)
	}
}

func TestAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"Page not found"}`))
	}))
	defer ts.Close()

	c := newTestClient(ts)

	_, err := c.CreatePage("12345", "parent1", "Test", "<p>test</p>")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

func TestBasicAuth(t *testing.T) {
	var gotUser, gotPass string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		jsonResponse(t, w, 200, map[string]any{
			"results": []map[string]any{},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	c.SearchPagesByTitle("test")

	if gotUser != "test@example.com" {
		t.Errorf("expected user 'test@example.com', got %q", gotUser)
	}
	if gotPass != "test-token" {
		t.Errorf("expected password 'test-token', got %q", gotPass)
	}
}

// TestPageLifecycle tests create → read → update → read flow.
func TestPageLifecycle(t *testing.T) {
	version := 1
	storedBody := ""

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/pages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		payload := readBody(t, r)
		bodyMap := payload["body"].(map[string]any)
		storedBody = bodyMap["value"].(string)
		jsonResponse(t, w, 200, map[string]any{
			"id":      "77777",
			"title":   payload["title"],
			"spaceId": payload["spaceId"],
			"version": map[string]any{"number": version},
		})
	})
	mux.HandleFunc("/api/v2/pages/77777", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			jsonResponse(t, w, 200, map[string]any{
				"id":      "77777",
				"title":   "Lifecycle Page",
				"spaceId": "12345",
				"version": map[string]any{"number": version},
				"body": map[string]any{
					"storage": map[string]any{
						"value": storedBody,
					},
				},
			})
		case "PUT":
			payload := readBody(t, r)
			bodyMap := payload["body"].(map[string]any)
			storedBody = bodyMap["value"].(string)
			version++
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := newTestClient(ts)

	// Create
	page, err := c.CreatePage("12345", "parent1", "Lifecycle Page", "<p>v1</p>")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if page.ID != "77777" {
		t.Fatalf("expected ID 77777, got %s", page.ID)
	}

	// Read
	readPage, body, err := c.GetPageBody("77777")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if body != "<p>v1</p>" {
		t.Errorf("expected body '<p>v1</p>', got %q", body)
	}

	// Update
	err = c.UpdatePage("77777", "Lifecycle Page", "<p>v2</p>", readPage.Version.Number)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	// Read again
	_, body, err = c.GetPageBody("77777")
	if err != nil {
		t.Fatalf("read after update: %v", err)
	}
	if body != "<p>v2</p>" {
		t.Errorf("expected body '<p>v2</p>', got %q", body)
	}
}

// TestBlogPostLifecycle tests create → read → update → read for blog posts.
func TestBlogPostLifecycle(t *testing.T) {
	version := 1
	storedBody := ""

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/blogposts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		payload := readBody(t, r)
		bodyMap := payload["body"].(map[string]any)
		storedBody = bodyMap["value"].(string)
		jsonResponse(t, w, 200, map[string]any{
			"id":      "66666",
			"title":   payload["title"],
			"spaceId": payload["spaceId"],
			"version": map[string]any{"number": version},
		})
	})
	mux.HandleFunc("/api/v2/blogposts/66666", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			jsonResponse(t, w, 200, map[string]any{
				"id":      "66666",
				"title":   "Blog Lifecycle",
				"spaceId": "12345",
				"version": map[string]any{"number": version},
				"body": map[string]any{
					"storage": map[string]any{
						"value": storedBody,
					},
				},
			})
		case "PUT":
			payload := readBody(t, r)
			bodyMap := payload["body"].(map[string]any)
			storedBody = bodyMap["value"].(string)
			version++
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := newTestClient(ts)

	// Create blog post
	post, err := c.CreateBlogPost("12345", "Blog Lifecycle", "<p>blog v1</p>")
	if err != nil {
		t.Fatalf("create blog: %v", err)
	}
	if post.ID != "66666" {
		t.Fatalf("expected ID 66666, got %s", post.ID)
	}

	// Read blog post
	readPost, body, err := c.GetBlogPostBody("66666")
	if err != nil {
		t.Fatalf("read blog: %v", err)
	}
	if body != "<p>blog v1</p>" {
		t.Errorf("expected body '<p>blog v1</p>', got %q", body)
	}

	// Update blog post
	err = c.UpdateBlogPost("66666", "Blog Lifecycle", "<p>blog v2</p>", readPost.Version.Number)
	if err != nil {
		t.Fatalf("update blog: %v", err)
	}

	// Read again
	_, body, err = c.GetBlogPostBody("66666")
	if err != nil {
		t.Fatalf("read blog after update: %v", err)
	}
	if body != "<p>blog v2</p>" {
		t.Errorf("expected body '<p>blog v2</p>', got %q", body)
	}
}
