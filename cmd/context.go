package cmd

import (
	"context"

	"github.com/spf13/cobra"
)

func commandContext(cmd *cobra.Command) context.Context {
	if cmd == nil || cmd.Context() == nil {
		return context.Background()
	}
	return cmd.Context()
}
