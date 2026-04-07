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
