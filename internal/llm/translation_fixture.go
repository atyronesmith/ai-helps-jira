package llm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
)

// TranslationFixture freezes ambient translation inputs for reproducible replays.
type TranslationFixture struct {
	Now          time.Time           `json:"now"`
	Metadata     TranslationMetadata `json:"metadata"`
	ProjectMap   map[string]string   `json:"project_map"`
	FieldAliases map[string]string   `json:"field_aliases"`
}

// CaptureTranslationFixture snapshots the clock, cached metadata, and configured
// project and field vocabularies used by a translation.
func CaptureTranslationFixture(ctx context.Context, cfg *config.Config, projects []jira.Project) (TranslationFixture, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	aliases, err := fieldAliases()
	if err != nil {
		return TranslationFixture{}, err
	}
	return TranslationFixture{Now: time.Now(), Metadata: metadataFromContext(ctx), ProjectMap: buildProjectMapWithProjects(cfg, projects), FieldAliases: aliases}, nil
}

// WithTranslationFixture makes standard translation entry points use this
// frozen snapshot while retaining their configured lazy provider.
func WithTranslationFixture(ctx context.Context, fixture TranslationFixture) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, translationFixtureKey{}, fixture)
	return context.WithValue(ctx, metadataKey{}, fixture.Metadata)
}

type countedFixtureProvider struct{ provider nl2jql.Provider }

func (p countedFixtureProvider) Generate(ctx context.Context, system, prompt string) (string, error) {
	countProviderCall(ctx)
	return p.provider.Generate(ctx, system, prompt)
}

// GenerateJQLWithFixture runs the production translation and scope checks from
// frozen inputs, using only the caller-supplied fallback provider.
func GenerateJQLWithFixture(ctx context.Context, cfg *config.Config, input string, fixture TranslationFixture, provider nl2jql.Provider) (*QueryResult, error) {
	ctx = WithTranslationFixture(ctx, fixture)
	return translateCascade(ctx, cfg, input, nil, func(cfg *config.Config, _ []jira.Project) (*nl2jql.Translator, error) {
		if fixture.Now.IsZero() {
			return nil, errors.New("translation fixture clock is empty")
		}
		if provider == nil {
			return nil, nl2jql.ErrNilProvider
		}
		fallback, err := nl2jql.NewFallbackEngine(countedFixtureProvider{provider: provider})
		if err != nil {
			return nil, err
		}
		normalizer := nl2jql.NewNormalizer()
		normalizer.Now = func() time.Time { return fixture.Now }
		return translatorWithInputs(cfg, fallback, normalizer, fixture.Metadata, fixture.ProjectMap, fixture.FieldAliases, slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
}

func fixtureFromContext(ctx context.Context) (TranslationFixture, bool) {
	f, ok := ctx.Value(translationFixtureKey{}).(TranslationFixture)
	return f, ok
}
