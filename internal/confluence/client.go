package confluence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

// Client provides Confluence Cloud REST API access.
type Client struct {
	http  *http.Client
	base  string // e.g. https://yourcompany.atlassian.net/wiki
	email string
	token string
}

// NewClient creates a Confluence client using the same Atlassian credentials as JIRA.
func NewClient(cfg *config.Config) *Client {
	return &Client{
		http:  &http.Client{Timeout: 30 * time.Second},
		base:  cfg.JiraServer + "/wiki",
		email: cfg.JiraEmail,
		token: cfg.JiraAPIToken,
	}
}

func (c *Client) doRequest(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.email, c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("Confluence API %s %s: %s (status %d)",
			method, path, truncate(string(respBody), 300), resp.StatusCode)
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Page represents a Confluence page.
type Page struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	SpaceID  string `json:"spaceId"`
	SpaceKey string `json:"-"` // populated from v1 API
	Version  struct {
		Number int `json:"number"`
	} `json:"version"`
}

// GetPage fetches a page by ID, including its space key.
func (c *Client) GetPage(pageID string) (*Page, error) {
	slog.Info("fetching confluence page", "id", pageID)

	// Use v1 API to get space key (v2 only returns spaceId)
	var v1resp struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Space struct {
			Key string `json:"key"`
		} `json:"space"`
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
	}
	if err := c.doRequest("GET", fmt.Sprintf("/rest/api/content/%s?expand=space,version", pageID), nil, &v1resp); err != nil {
		return nil, fmt.Errorf("get page %s: %w", pageID, err)
	}

	// Also get spaceId from v2 API
	var page Page
	if err := c.doRequest("GET", "/api/v2/pages/"+pageID, nil, &page); err != nil {
		return nil, fmt.Errorf("get page v2 %s: %w", pageID, err)
	}

	page.SpaceKey = v1resp.Space.Key
	return &page, nil
}

// GetPageByTitle finds a page by title within a space.
func (c *Client) GetPageByTitle(spaceID, title string) (*Page, error) {
	slog.Info("searching confluence page", "space", spaceID, "title", title)
	path := fmt.Sprintf("/api/v2/pages?space-id=%s&title=%s&status=current",
		spaceID, url.QueryEscape(title))

	var resp struct {
		Results []Page `json:"results"`
	}
	if err := c.doRequest("GET", path, nil, &resp); err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 {
		return nil, nil
	}
	return &resp.Results[0], nil
}

// CreatePage creates a new page under a parent.
func (c *Client) CreatePage(spaceID, parentID, title, body string) (*Page, error) {
	slog.Info("creating confluence page", "space", spaceID, "parent", parentID, "title", title)

	payload := map[string]any{
		"spaceId":  spaceID,
		"parentId": parentID,
		"status":   "current",
		"title":    title,
		"body": map[string]any{
			"representation": "storage",
			"value":          body,
		},
	}

	var page Page
	if err := c.doRequest("POST", "/api/v2/pages", payload, &page); err != nil {
		return nil, fmt.Errorf("create page: %w", err)
	}
	slog.Info("confluence page created", "id", page.ID, "title", title)
	return &page, nil
}

// UpdatePage overwrites an existing page's content.
func (c *Client) UpdatePage(pageID, title, body string, version int) error {
	slog.Info("updating confluence page", "id", pageID, "title", title, "version", version)

	payload := map[string]any{
		"id":     pageID,
		"status": "current",
		"title":  title,
		"version": map[string]any{
			"number": version + 1,
		},
		"body": map[string]any{
			"representation": "storage",
			"value":          body,
		},
	}

	if err := c.doRequest("PUT", "/api/v2/pages/"+pageID, payload, nil); err != nil {
		return fmt.Errorf("update page %s: %w", pageID, err)
	}
	slog.Info("confluence page updated", "id", pageID)
	return nil
}

// GetPageBody fetches a page with its storage body.
func (c *Client) GetPageBody(pageID string) (*Page, string, error) {
	slog.Info("fetching confluence page body", "id", pageID)

	var resp struct {
		Page
		Body struct {
			Storage struct {
				Value string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
	}
	path := fmt.Sprintf("/api/v2/pages/%s?body-format=storage", pageID)
	if err := c.doRequest("GET", path, nil, &resp); err != nil {
		return nil, "", fmt.Errorf("get page body %s: %w", pageID, err)
	}
	page := &Page{
		ID:      resp.ID,
		Title:   resp.Title,
		SpaceID: resp.SpaceID,
		Version: resp.Version,
	}
	return page, resp.Body.Storage.Value, nil
}

// SearchPagesByTitle finds pages by title across all spaces.
func (c *Client) SearchPagesByTitle(title string) ([]Page, error) {
	slog.Info("searching confluence pages", "title", title)
	path := fmt.Sprintf("/api/v2/pages?title=%s&status=current", url.QueryEscape(title))

	var resp struct {
		Results []Page `json:"results"`
	}
	if err := c.doRequest("GET", path, nil, &resp); err != nil {
		return nil, fmt.Errorf("search pages: %w", err)
	}
	return resp.Results, nil
}

// PageAnalytics holds view statistics for a Confluence page.
type PageAnalytics struct {
	PageID        string
	Title         string
	TotalViews    int
	UniqueViewers int
}

// GetChildPages returns the direct child pages of a parent page.
func (c *Client) GetChildPages(parentID string) ([]Page, error) {
	slog.Info("fetching child pages", "parent", parentID)

	var resp struct {
		Results []Page `json:"results"`
	}
	path := fmt.Sprintf("/api/v2/pages/%s/children?limit=250", parentID)
	if err := c.doRequest("GET", path, nil, &resp); err != nil {
		return nil, fmt.Errorf("get child pages %s: %w", parentID, err)
	}
	return resp.Results, nil
}

// GetPageViews returns the total view count for a page.
func (c *Client) GetPageViews(pageID string) (int, error) {
	var resp struct {
		Count int `json:"count"`
	}
	path := fmt.Sprintf("/rest/api/analytics/content/%s/views", pageID)
	if err := c.doRequest("GET", path, nil, &resp); err != nil {
		return 0, fmt.Errorf("get page views %s: %w", pageID, err)
	}
	return resp.Count, nil
}

// GetPageViewers returns the unique viewer count for a page.
func (c *Client) GetPageViewers(pageID string) (int, error) {
	var resp struct {
		Count int `json:"count"`
	}
	path := fmt.Sprintf("/rest/api/analytics/content/%s/viewers", pageID)
	if err := c.doRequest("GET", path, nil, &resp); err != nil {
		return 0, fmt.Errorf("get page viewers %s: %w", pageID, err)
	}
	return resp.Count, nil
}

// GetPageAnalytics fetches a page's title, total views, and unique viewers.
func (c *Client) GetPageAnalytics(pageID string) (*PageAnalytics, error) {
	// Get page title via v2 API (lightweight, no body fetch)
	var page Page
	if err := c.doRequest("GET", "/api/v2/pages/"+pageID, nil, &page); err != nil {
		return nil, fmt.Errorf("get page %s: %w", pageID, err)
	}

	views, err := c.GetPageViews(pageID)
	if err != nil {
		return nil, err
	}

	viewers, err := c.GetPageViewers(pageID)
	if err != nil {
		return nil, err
	}

	return &PageAnalytics{
		PageID:        pageID,
		Title:         page.Title,
		TotalViews:    views,
		UniqueViewers: viewers,
	}, nil
}

// ResolveContentID fetches the content ID from a v1 API content lookup.
// Used to resolve tiny links like /wiki/x/tZILFw to a real page ID.
func (c *Client) ResolveContentID(contentID string) (*Page, error) {
	slog.Info("resolving confluence content", "id", contentID)

	var resp struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Space struct {
			Key string `json:"key"`
		} `json:"space"`
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
	}
	path := fmt.Sprintf("/rest/api/content/%s?expand=space,version", contentID)
	if err := c.doRequest("GET", path, nil, &resp); err != nil {
		return nil, err
	}
	// We need spaceId for v2 API — look it up from space key
	return &Page{
		ID:    resp.ID,
		Title: resp.Title,
		Version: struct {
			Number int `json:"number"`
		}{Number: resp.Version.Number},
	}, nil
}

// Comment represents a Confluence page comment.
type Comment struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Body   string `json:"-"` // populated from nested response
	Author string `json:"-"` // populated from nested response
}

// SearchCQL searches Confluence content using CQL (Confluence Query Language).
func (c *Client) SearchCQL(cql string, limit int) ([]Page, error) {
	slog.Info("searching confluence", "cql", cql, "limit", limit)
	if limit <= 0 {
		limit = 25
	}
	path := fmt.Sprintf("/rest/api/content/search?cql=%s&limit=%d&expand=version",
		url.QueryEscape(cql), limit)

	var resp struct {
		Results []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Space struct {
				Key string `json:"key"`
			} `json:"space"`
			Version struct {
				Number int `json:"number"`
			} `json:"version"`
		} `json:"results"`
	}
	if err := c.doRequest("GET", path, nil, &resp); err != nil {
		return nil, fmt.Errorf("search CQL: %w", err)
	}

	pages := make([]Page, len(resp.Results))
	for i, r := range resp.Results {
		pages[i] = Page{
			ID:       r.ID,
			Title:    r.Title,
			SpaceKey: r.Space.Key,
			Version:  r.Version,
		}
	}
	return pages, nil
}

// GetPagesInSpace lists pages in a space, optionally filtered by title substring.
func (c *Client) GetPagesInSpace(spaceKey string, limit int) ([]Page, error) {
	slog.Info("listing pages in space", "space", spaceKey, "limit", limit)
	if limit <= 0 {
		limit = 25
	}

	cql := fmt.Sprintf("space = %q AND type = page ORDER BY lastModified DESC", spaceKey)
	return c.SearchCQL(cql, limit)
}

// GetPageComments fetches footer comments on a page.
func (c *Client) GetPageComments(pageID string) ([]Comment, error) {
	slog.Info("fetching page comments", "page_id", pageID)

	var resp struct {
		Results []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Body  struct {
				View struct {
					Value string `json:"value"`
				} `json:"view"`
			} `json:"body"`
			Version struct {
				By struct {
					DisplayName string `json:"displayName"`
				} `json:"by"`
			} `json:"version"`
		} `json:"results"`
	}
	path := fmt.Sprintf("/rest/api/content/%s/child/comment?expand=body.view,version", pageID)
	if err := c.doRequest("GET", path, nil, &resp); err != nil {
		return nil, fmt.Errorf("get comments %s: %w", pageID, err)
	}

	comments := make([]Comment, len(resp.Results))
	for i, r := range resp.Results {
		comments[i] = Comment{
			ID:     r.ID,
			Title:  r.Title,
			Body:   r.Body.View.Value,
			Author: r.Version.By.DisplayName,
		}
	}
	return comments, nil
}

// BlogPost represents a Confluence blog post.
type BlogPost struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	SpaceID string `json:"spaceId"`
	Version struct {
		Number int `json:"number"`
	} `json:"version"`
}

// CreateBlogPost creates a new blog post in a space.
func (c *Client) CreateBlogPost(spaceID, title, body string) (*BlogPost, error) {
	slog.Info("creating confluence blog post", "space", spaceID, "title", title)

	payload := map[string]any{
		"spaceId": spaceID,
		"status":  "current",
		"title":   title,
		"body": map[string]any{
			"representation": "storage",
			"value":          body,
		},
	}

	var post BlogPost
	if err := c.doRequest("POST", "/api/v2/blogposts", payload, &post); err != nil {
		return nil, fmt.Errorf("create blog post: %w", err)
	}
	slog.Info("confluence blog post created", "id", post.ID, "title", title)
	return &post, nil
}

// UpdateBlogPost updates an existing blog post's content.
func (c *Client) UpdateBlogPost(postID, title, body string, version int) error {
	slog.Info("updating confluence blog post", "id", postID, "title", title, "version", version)

	payload := map[string]any{
		"id":     postID,
		"status": "current",
		"title":  title,
		"version": map[string]any{
			"number": version + 1,
		},
		"body": map[string]any{
			"representation": "storage",
			"value":          body,
		},
	}

	if err := c.doRequest("PUT", "/api/v2/blogposts/"+postID, payload, nil); err != nil {
		return fmt.Errorf("update blog post %s: %w", postID, err)
	}
	slog.Info("confluence blog post updated", "id", postID)
	return nil
}

// GetBlogPostBody fetches a blog post with its storage body.
func (c *Client) GetBlogPostBody(postID string) (*BlogPost, string, error) {
	slog.Info("fetching confluence blog post body", "id", postID)

	var resp struct {
		BlogPost
		Body struct {
			Storage struct {
				Value string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
	}
	path := fmt.Sprintf("/api/v2/blogposts/%s?body-format=storage", postID)
	if err := c.doRequest("GET", path, nil, &resp); err != nil {
		return nil, "", fmt.Errorf("get blog post body %s: %w", postID, err)
	}
	post := &BlogPost{
		ID:      resp.ID,
		Title:   resp.Title,
		SpaceID: resp.SpaceID,
		Version: resp.Version,
	}
	return post, resp.Body.Storage.Value, nil
}

// SearchBlogPostsByTitle finds blog posts by title across all spaces.
func (c *Client) SearchBlogPostsByTitle(title string) ([]BlogPost, error) {
	slog.Info("searching confluence blog posts", "title", title)

	cql := fmt.Sprintf("type = blogpost AND title = %q", title)
	path := fmt.Sprintf("/rest/api/content/search?cql=%s&limit=10&expand=version",
		url.QueryEscape(cql))

	var resp struct {
		Results []struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Version struct {
				Number int `json:"number"`
			} `json:"version"`
		} `json:"results"`
	}
	if err := c.doRequest("GET", path, nil, &resp); err != nil {
		return nil, fmt.Errorf("search blog posts: %w", err)
	}

	posts := make([]BlogPost, len(resp.Results))
	for i, r := range resp.Results {
		posts[i] = BlogPost{
			ID:      r.ID,
			Title:   r.Title,
			Version: r.Version,
		}
	}
	return posts, nil
}

// AddLabel adds a label to a page.
func (c *Client) AddLabel(pageID, label string) error {
	slog.Info("adding label", "page_id", pageID, "label", label)

	payload := []map[string]string{
		{"prefix": "global", "name": label},
	}
	path := fmt.Sprintf("/rest/api/content/%s/label", pageID)
	return c.doRequest("POST", path, payload, nil)
}
