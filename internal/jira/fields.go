package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
)

var fieldIDPattern = regexp.MustCompile(`^(customfield_[0-9]+|[A-Za-z][A-Za-z0-9]*)$`)

type apiField struct {
	ID          string   `json:"id"`
	StableID    string   `json:"stableId"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	ClauseNames []string `json:"clauseNames"`
	SearcherKey string   `json:"searcherKey"`
	Schema      struct {
		Type  string `json:"type"`
		Items string `json:"items"`
	} `json:"schema"`
}

// ListFields reads Jira's site-visible field catalog. The response does not
// establish project/work-type applicability, so that remains unknown.
func (c *Client) ListFields(ctx context.Context) ([]fieldmeta.Field, error) {
	var response []apiField
	if err := c.doRequestContext(ctx, http.MethodGet, "/rest/api/3/field", nil, &response); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(response))
	out := make([]fieldmeta.Field, 0, len(response))
	for i, x := range response {
		if x.ID == "" || x.Name == "" || !fieldIDPattern.MatchString(x.ID) {
			return nil, fmt.Errorf("invalid field metadata at index %d", i)
		}
		if seen[x.ID] {
			return nil, fmt.Errorf("duplicate Jira field ID %q", x.ID)
		}
		seen[x.ID] = true
		aliases := make([]string, 0, len(x.ClauseNames))
		for _, a := range x.ClauseNames {
			if a != "" {
				aliases = append(aliases, a)
			}
		}
		out = append(out, fieldmeta.Field{
			ID: x.ID, StableID: x.StableID, Name: x.Name, Description: x.Description,
			Type: x.Schema.Type, ItemType: x.Schema.Items, Searcher: x.SearcherKey,
			Aliases: aliases, ApplicabilityKnown: false,
		})
	}
	return out, nil
}

type flexibleID string

func (id *flexibleID) UnmarshalJSON(raw []byte) error {
	var s string
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		*id = flexibleID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return err
	}
	*id = flexibleID(n.String())
	return nil
}

type projectFieldValue struct {
	FieldID    string     `json:"fieldId"`
	ProjectID  flexibleID `json:"projectId"`
	WorkTypeID flexibleID `json:"workTypeId"`
}

type projectFieldsPage struct {
	IsLast     *bool               `json:"isLast"`
	StartAt    int                 `json:"startAt"`
	MaxResults int                 `json:"maxResults"`
	Total      int                 `json:"total"`
	Values     []projectFieldValue `json:"values"`
}

type metadataProject struct {
	ID  flexibleID `json:"id"`
	Key string     `json:"key"`
}

type metadataWorkType struct {
	ID   flexibleID `json:"id"`
	Name string     `json:"name"`
}

// ResolveMetadataScope verifies the display labels associated with numeric
// project and work type IDs before callers store them as applicability claims.
func (c *Client) ResolveMetadataScope(ctx context.Context, projectID, workTypeID string) (string, string, error) {
	if _, err := strconv.ParseUint(projectID, 10, 64); err != nil {
		return "", "", fmt.Errorf("invalid Jira project ID %q: %w", projectID, err)
	}
	if _, err := strconv.ParseUint(workTypeID, 10, 64); err != nil {
		return "", "", fmt.Errorf("invalid Jira work type ID %q: %w", workTypeID, err)
	}
	var project metadataProject
	if err := c.doRequestContext(ctx, http.MethodGet, "/rest/api/3/project/"+projectID, nil, &project); err != nil {
		return "", "", err
	}
	if string(project.ID) != projectID || project.Key == "" {
		return "", "", fmt.Errorf("Jira project lookup did not match requested project ID %q", projectID)
	}
	var workType metadataWorkType
	if err := c.doRequestContext(ctx, http.MethodGet, "/rest/api/3/issuetype/"+workTypeID, nil, &workType); err != nil {
		return "", "", err
	}
	if string(workType.ID) != workTypeID || workType.Name == "" {
		return "", "", fmt.Errorf("Jira work type lookup did not match requested work type ID %q", workTypeID)
	}
	return project.Key, workType.Name, nil
}

// ListProjectFieldIDs returns field IDs explicitly associated with one Jira
// project and work type. It follows bounded pagination and validates every
// response row against the requested scope before returning any data.
func (c *Client) ListProjectFieldIDs(ctx context.Context, projectID, workTypeID string) ([]string, error) {
	if _, err := strconv.ParseUint(projectID, 10, 64); err != nil {
		return nil, fmt.Errorf("invalid Jira project ID %q: %w", projectID, err)
	}
	if _, err := strconv.ParseUint(workTypeID, 10, 64); err != nil {
		return nil, fmt.Errorf("invalid Jira work type ID %q: %w", workTypeID, err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	const pageSize, maxPages = 100, 100
	seen := make(map[string]struct{})
	var result []string
	startAt := 0
	for pageNumber := 0; pageNumber < maxPages; pageNumber++ {
		query := url.Values{
			"projectId":  {projectID},
			"workTypeId": {workTypeID},
			"startAt":    {strconv.Itoa(startAt)},
			"maxResults": {strconv.Itoa(pageSize)},
		}
		path := "/rest/api/3/projects/fields?" + query.Encode()
		var page projectFieldsPage
		if err := c.doRequestContext(ctx, http.MethodGet, path, nil, &page); err != nil {
			return nil, err
		}
		if page.IsLast == nil {
			return nil, fmt.Errorf("Jira project fields page %d omitted isLast", pageNumber)
		}
		if page.StartAt != startAt {
			return nil, fmt.Errorf("Jira project fields page started at %d, requested %d", page.StartAt, startAt)
		}
		for _, value := range page.Values {
			if value.FieldID == "" {
				return nil, fmt.Errorf("Jira project fields page %d contained an empty fieldId", pageNumber)
			}
			if string(value.ProjectID) != projectID || string(value.WorkTypeID) != workTypeID {
				return nil, fmt.Errorf("Jira project fields page %d returned a row outside requested project/work type", pageNumber)
			}
			if _, duplicate := seen[value.FieldID]; duplicate {
				return nil, fmt.Errorf("Jira project fields response contained duplicate field ID %q", value.FieldID)
			}
			seen[value.FieldID] = struct{}{}
			result = append(result, value.FieldID)
		}
		if *page.IsLast {
			return result, nil
		}
		if len(page.Values) == 0 {
			return nil, fmt.Errorf("Jira project fields pagination stalled at startAt %d", startAt)
		}
		next := startAt + len(page.Values)
		if next <= startAt {
			return nil, fmt.Errorf("Jira project fields pagination stalled at startAt %d", startAt)
		}
		startAt = next
	}
	return nil, fmt.Errorf("Jira project fields exceeded the %d-page limit", maxPages)
}
