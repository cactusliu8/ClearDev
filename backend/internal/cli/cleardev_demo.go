package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevdemo"
)

func newClearDevDemoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "demo",
		Short: "Run the frozen ClearDev full-stack demonstration in an isolated directory",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return usageError{errors.New("ao cleardev demo does not accept mode, path, check, session, candidate, or completion arguments")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := cleardevdemo.Run(cmd.Context(), cleardevdemo.Options{
				Stdout: cmd.OutOrStdout(),
				Stderr: cmd.ErrOrStderr(),
			})
			if err != nil {
				if result.Layout.EvidenceDir != "" {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "evidence directory: %s\n", result.Layout.EvidenceDir)
				}
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]any{
				"evidenceDir":   result.Layout.EvidenceDir,
				"requirementId": result.Evidence.RequirementID,
				"mode":          result.Evidence.Mode,
				"phase":         result.Evidence.ProgressPhase,
				"integration":   result.Evidence.IntegrationSHA,
			})
		},
	}
	cmd.AddCommand(newClearDevDemoVerifyCommand())
	return cmd
}

func newClearDevDemoVerifyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <evidence-dir>",
		Short: "Check a previously written ClearDev demonstration evidence pack offline",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
				return usageError{errors.New("usage: ao cleardev demo verify <evidence-dir>")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := strings.TrimSpace(args[0])
			result, err := cleardevdemo.VerifyEvidenceDir(dir)
			if err != nil {
				return err
			}
			payload := map[string]any{
				"ok":            true,
				"evidenceDir":   dir,
				"schemaVersion": result.SchemaVersion,
				"scope":         cleardevdemo.EvidencePackConsistencyScope,
			}
			if result.Legacy {
				payload["legacy"] = true
			}
			return writeJSON(cmd.OutOrStdout(), payload)
		},
	}
}
