package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

var metadataCmd = &cobra.Command{Use: "metadata", Short: "Manage cached Jira field metadata"}
var metadataRefreshCmd = &cobra.Command{Use: "refresh", Short: "Refresh the Jira field catalog", Args: cobra.NoArgs, RunE: runMetadataRefresh}

var (
	flagMetadataProjectID    string
	flagMetadataWorkTypeID   string
	flagMetadataWorkTypeName string
)

func init() {
	metadataRefreshCmd.Flags().StringVar(&flagMetadataProjectID, "project-id", "", "Numeric Jira project ID for scoped applicability metadata.")
	metadataRefreshCmd.Flags().StringVar(&flagMetadataWorkTypeID, "work-type-id", "", "Numeric Jira work type ID for scoped applicability metadata.")
	metadataRefreshCmd.Flags().StringVar(&flagMetadataWorkTypeName, "work-type-name", "", "Display name of the work type selected by --work-type-id.")
	metadataCmd.AddCommand(metadataRefreshCmd)
	rootCmd.AddCommand(metadataCmd)
}

func runMetadataRefresh(cmd *cobra.Command, _ []string) error {
	if err := validateMetadataScope(flagMetadataProjectID, flagMetadataWorkTypeID, flagMetadataWorkTypeName); err != nil {
		return err
	}
	cfg, err := config.LoadJIRAOnly(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()
	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}
	client = client.WithContext(commandContext(cmd))
	fields, err := loadMetadataFields(commandContext(cmd), client, cfg.JiraProject,
		flagMetadataProjectID, flagMetadataWorkTypeID, flagMetadataWorkTypeName)
	if err != nil {
		return err
	}
	db, err := cache.OpenForConfig(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	if err = db.ReplaceFields(commandContext(cmd), fields, time.Now().UTC()); err != nil {
		return err
	}
	if flagMetadataProjectID == "" {
		pterm.Printf("Refreshed %d site-visible Jira fields; project/work-type applicability remains unknown.\n", len(fields))
	} else {
		pterm.Printf("Refreshed %d Jira fields for project %s and work type %s.\n", len(fields), cfg.JiraProject, flagMetadataWorkTypeName)
	}
	return nil
}

func validateMetadataScope(projectID, workTypeID, workTypeName string) error {
	provided := 0
	for _, value := range []string{projectID, workTypeID, workTypeName} {
		if value != "" {
			provided++
		}
	}
	if provided != 0 && provided != 3 {
		return fmt.Errorf("--project-id, --work-type-id, and --work-type-name must be provided together")
	}
	return nil
}

func loadMetadataFields(ctx context.Context, client *jira.Client, projectKey, projectID, workTypeID, workTypeName string) ([]fieldmeta.Field, error) {
	if err := validateMetadataScope(projectID, workTypeID, workTypeName); err != nil {
		return nil, err
	}
	fields, err := client.ListFields(ctx)
	if err != nil || projectID == "" {
		return fields, err
	}
	actualProjectKey, actualWorkTypeName, err := client.ResolveMetadataScope(ctx, projectID, workTypeID)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(actualProjectKey, projectKey) {
		return nil, fmt.Errorf("project ID %s resolves to %s, but configured project is %s", projectID, actualProjectKey, projectKey)
	}
	if !strings.EqualFold(actualWorkTypeName, workTypeName) {
		return nil, fmt.Errorf("work type ID %s resolves to %s, but --work-type-name is %s", workTypeID, actualWorkTypeName, workTypeName)
	}
	applicableIDs, err := client.ListProjectFieldIDs(ctx, projectID, workTypeID)
	if err != nil {
		return nil, err
	}
	applicable := make(map[string]struct{}, len(applicableIDs))
	for _, id := range applicableIDs {
		applicable[id] = struct{}{}
	}
	result := make([]fieldmeta.Field, 0, len(applicable))
	for _, field := range fields {
		if _, ok := applicable[field.ID]; !ok {
			continue
		}
		field.ApplicabilityKnown = true
		field.Projects = []string{actualProjectKey, projectID}
		field.IssueTypes = []string{actualWorkTypeName, workTypeID}
		result = append(result, field)
		delete(applicable, field.ID)
	}
	if len(applicable) > 0 {
		for id := range applicable {
			return nil, fmt.Errorf("Jira project fields endpoint returned field %q missing from site field catalog", id)
		}
	}
	return result, nil
}
