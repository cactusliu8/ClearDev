package cli

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

func newClearDevStageTrialCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{Use: "stage-trial <start|status|stop|run> <requirement-id> [step-id]", Short: "Operate the owning Stage Reviewer's controlled trial",
		Args: func(_ *cobra.Command, args []string) error {
			valid := false
			if len(args) == 2 {
				valid = args[0] == "start" || args[0] == "status" || args[0] == "stop"
			} else if len(args) == 3 {
				valid = args[0] == "run" && strings.TrimSpace(args[2]) != ""
			}
			if !valid || strings.TrimSpace(args[1]) == "" {
				return usageError{errors.New("requires start/status/stop and a requirement id, or run and a requirement id and frozen step id")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID, capability, err := currentBrowserIdentity()
			if err != nil {
				return err
			}
			action := args[0]
			if action == "run" {
				action = "run:" + args[2]
			}
			var response any
			timeout := commandTimeout
			if args[0] == "run" {
				timeout = 0
			} // The backend enforces the frozen operation deadline; do not cancel it at the generic CLI limit.
			err = ctx.doJSONPathWithHeadersAndTimeout(cmd.Context(), http.MethodPost,
				"/api/v1/cleardev/requirements/"+url.PathEscape(args[1])+"/stage-trial",
				map[string]string{"sessionId": sessionID, "action": action}, &response,
				map[string]string{browserCapabilityHeader: capability}, timeout)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), response)
		},
	}
	return cmd
}
