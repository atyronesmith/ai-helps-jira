package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
)

// TranslationMetadata is a snapshot of the permission-filtered tenant catalog.
// Stale snapshots remain useful offline; freshness is disclosed in the result.
type TranslationMetadata struct {
	Fields    []fieldmeta.Field
	FetchedAt time.Time
}
type metadataKey struct{}

func WithTranslationMetadata(ctx context.Context, c *cache.Cache) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil {
		return ctx
	}
	fields, at, err := c.GetFields(ctx)
	if err != nil {
		return ctx
	}
	return context.WithValue(ctx, metadataKey{}, TranslationMetadata{Fields: fields, FetchedAt: at})
}
func metadataFromContext(ctx context.Context) TranslationMetadata {
	m, _ := ctx.Value(metadataKey{}).(TranslationMetadata)
	return m
}
func fieldAliases() (map[string]string, error) {
	var aliases map[string]string
	raw := config.GetEnvOrSecret("JIRA_FIELD_ALIASES")
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &aliases); err != nil {
			return nil, errors.New("JIRA_FIELD_ALIASES must be a JSON object mapping aliases to field IDs")
		}
	}
	for alias, target := range aliases {
		if alias == "" || target == "" {
			return nil, errors.New("JIRA_FIELD_ALIASES contains an empty alias or field ID")
		}
	}
	return aliases, nil
}

func schemaWithFields(cfg *config.Config, fields []fieldmeta.Field, aliases map[string]string) map[string]string {
	schema := buildSchema(cfg)
	for _, f := range fields {
		encoded, _ := json.Marshal(f)
		schema["field "+f.ID] = string(encoded)
	}
	for alias, target := range aliases {
		schema[fmt.Sprintf("field alias %s", alias)] = target
	}
	return schema
}
func stampMetadata(ctx context.Context, res *QueryResult) {
	m := metadataFromContext(ctx)
	if !m.FetchedAt.IsZero() {
		res.MetadataFetchedAt = m.FetchedAt
		res.MetadataApplicabilityKnown = len(m.Fields) > 0
		for _, f := range m.Fields {
			if !f.ApplicabilityKnown {
				res.MetadataApplicabilityKnown = false
				break
			}
		}
		res.MetadataStale = time.Since(m.FetchedAt) > 24*time.Hour
	}
}
