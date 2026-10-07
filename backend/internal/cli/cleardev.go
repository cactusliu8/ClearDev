package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// newClearDevCommand builds the ClearDev command group. ClearDev commands are
// thin clients over the daemon API; they do not access the ClearDev store.
func newClearDevCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cleardev",
		Short: "Manage ClearDev development requirements",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			return usageError{errors.New("requires a ClearDev subcommand")}
		},
	}
	cmd.AddCommand(newClearDevRequirementCommand(ctx))
	cmd.AddCommand(newClearDevStandardCommand(ctx))
	cmd.AddCommand(newClearDevExecutionCommand(ctx))
	cmd.AddCommand(newClearDevProgressCommand(ctx))
	cmd.AddCommand(newClearDevDemoCommand())
	cmd.AddCommand(newClearDevStageTrialCommand(ctx))
	return cmd
}

func newClearDevProgressCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use:   "progress <project-id>",
		Short: "Show derived ClearDev progress for an AO project",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError{errors.New("requires exactly 1 argument")}
			}
			if strings.TrimSpace(args[0]) == "" {
				return usageError{errors.New("usage: AO project id is required")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			var response any
			if err := ctx.getJSON(cmd.Context(), "cleardev/projects/"+url.PathEscape(id)+"/progress", &response); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), response)
		},
	}
}

func newClearDevStandardCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "standard",
		Short: "Run a ClearDev standard flow",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			return usageError{errors.New("requires a standard subcommand")}
		},
	}
	cmd.AddCommand(newClearDevStandardStartCommand(ctx))
	return cmd
}

func newClearDevStandardStartCommand(ctx *commandContext) *cobra.Command {
	return newClearDevFlowStartCommand(ctx, "standard-runs", "Start a ClearDev standard flow")
}

func newClearDevFlowStartCommand(ctx *commandContext, runPath, short string) *cobra.Command {
	return &cobra.Command{
		Use:   "start <requirement-id>",
		Short: short,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError{errors.New("requires exactly 1 argument")}
			}
			if strings.TrimSpace(args[0]) == "" {
				return usageError{errors.New("usage: ClearDev requirement id is required")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			var response any
			// nil is intentional: postJSON uses http.NoBody, so this endpoint
			// receives a genuinely empty body rather than the JSON value null.
			if err := ctx.postJSON(cmd.Context(), "cleardev/requirements/"+url.PathEscape(id)+"/"+runPath, nil, &response); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), response)
		},
	}
}

func newClearDevExecutionCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "execution",
		Short: "Run a ClearDev complex execution",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			return usageError{errors.New("requires an execution subcommand")}
		},
	}
	cmd.AddCommand(newClearDevExecutionStartCommand(ctx))
	return cmd
}

func newClearDevExecutionStartCommand(ctx *commandContext) *cobra.Command {
	return newClearDevFlowStartCommand(ctx, "execution-runs", "Start a ClearDev complex execution")
}

func newClearDevRequirementCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "requirement",
		Short: "Manage ClearDev development requirements",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			return usageError{errors.New("requires a requirement subcommand")}
		},
	}
	cmd.AddCommand(newClearDevRequirementCreateCommand(ctx))
	cmd.AddCommand(newClearDevRequirementCreateComplexCommand(ctx))
	cmd.AddCommand(newClearDevRequirementClarifyCommand(ctx))
	cmd.AddCommand(newClearDevRequirementProposeDirectionCommand(ctx))
	cmd.AddCommand(newClearDevRequirementShowCommand(ctx))
	cmd.AddCommand(newClearDevRequirementExplainCommand(ctx))
	return cmd
}

func newClearDevRequirementExplainCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use:   "explain <requirement-id>",
		Short: "Ask the original Project Steward to explain current trusted progress",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError{errors.New("requires exactly 1 argument")}
			}
			if strings.TrimSpace(args[0]) == "" {
				return usageError{errors.New("usage: ClearDev requirement id is required")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			var response any
			if err := ctx.postJSON(cmd.Context(), "cleardev/requirements/"+url.PathEscape(id)+"/progress-explanations", nil, &response); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), response)
		},
	}
}

func newClearDevRequirementCreateCommand(ctx *commandContext) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a ClearDev development requirement from a JSON file",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file = strings.TrimSpace(file)
			if file == "" {
				return usageError{errors.New("--file is required")}
			}
			body, err := readClearDevJSONObject(file)
			if err != nil {
				return err
			}

			var response any
			if err := ctx.postJSON(cmd.Context(), "cleardev/requirements", body, &response); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), response)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "Path to the ClearDev development requirement JSON object (required)")
	return cmd
}

func newClearDevRequirementCreateComplexCommand(ctx *commandContext) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "create-complex",
		Short: "Create a ClearDev complex requirement from a JSON file",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file = strings.TrimSpace(file)
			if file == "" {
				return usageError{errors.New("--file is required")}
			}
			body, err := readClearDevJSONObject(file)
			if err != nil {
				return err
			}
			var response any
			if err := ctx.postJSON(cmd.Context(), "cleardev/requirements/complex", body, &response); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), response)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "Path to the ClearDev complex requirement JSON object (required)")
	return cmd
}

func newClearDevRequirementClarifyCommand(ctx *commandContext) *cobra.Command {
	return newClearDevRequirementFilePostCommand(
		ctx,
		"clarify <id>",
		"Submit answers for one unanswered ClearDev complex clarification round",
		"complex-clarifications",
		"Path to the clarification answers JSON object (required)",
	)
}

func newClearDevRequirementFilePostCommand(ctx *commandContext, use, short, endpointSuffix, fileDescription string) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError{errors.New("requires exactly 1 argument")}
			}
			if strings.TrimSpace(args[0]) == "" {
				return usageError{errors.New("usage: ClearDev requirement id is required")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			file = strings.TrimSpace(file)
			if file == "" {
				return usageError{errors.New("--file is required")}
			}
			body, err := readClearDevJSONObject(file)
			if err != nil {
				return err
			}
			id := strings.TrimSpace(args[0])
			var response any
			if err := ctx.postJSON(cmd.Context(), "cleardev/requirements/"+url.PathEscape(id)+"/"+endpointSuffix, body, &response); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), response)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", fileDescription)
	return cmd
}

func newClearDevRequirementProposeDirectionCommand(ctx *commandContext) *cobra.Command {
	return newClearDevRequirementFilePostCommand(
		ctx,
		"propose-direction <id>",
		"Hand a direction-change message to the original ClearDev Project Steward",
		"direction-intents",
		"Path to the direction-intent JSON object (required)",
	)
}

func newClearDevRequirementShowCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show a ClearDev development requirement",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError{errors.New("requires exactly 1 argument")}
			}
			if strings.TrimSpace(args[0]) == "" {
				return usageError{errors.New("usage: ClearDev requirement id is required")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			var response any
			if err := ctx.getJSON(cmd.Context(), "cleardev/requirements/"+url.PathEscape(id), &response); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), response)
		},
	}
}

// readClearDevJSONObject reads exactly one JSON object. File-system failures
// remain ordinary runtime errors; malformed, non-object, or multi-document
// input is a CLI usage error and therefore exits with code 2 before any API
// call is attempted.
func readClearDevJSONObject(path string) (json.RawMessage, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // --file is an explicit user-provided input path.
	if err != nil {
		return nil, fmt.Errorf("read ClearDev requirement file: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	var object map[string]json.RawMessage
	if err := dec.Decode(&object); err != nil || object == nil {
		return nil, usageError{errors.New("ClearDev requirement file must contain one valid JSON object")}
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, usageError{errors.New("ClearDev requirement file must contain exactly one JSON object")}
	}
	return json.RawMessage(raw), nil
}
