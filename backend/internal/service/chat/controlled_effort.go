package chat

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Every controlled relay (including corrections and restored sessions) binds the
// native option before recording or dispatching a message. Never silently fall back.
func (s *Service) bindControlledOpenCodeEffort(ctx context.Context, id domain.SessionID, controller *Controller) error {
	record, err := s.requireChatSession(ctx, id)
	if err != nil {
		return err
	}
	if record.Harness != domain.HarnessOpenCode || record.CreationIdempotencyKey == "" {
		return nil
	}
	choice, err := s.controlledExecutionChoice(ctx, record)
	if err != nil || choice == nil || choice.Effort == "" {
		return err
	}
	configurer, ok := controller.conv.(ports.ChatConfigOptionController)
	if !ok {
		return fmt.Errorf("controlled OpenCode effort is unavailable")
	}
	return bindOpenCodeEffort(ctx, configurer, choice.Model, choice.Effort)
}

func bindOpenCodeEffort(ctx context.Context, configurer ports.ChatConfigOptionController, model, effort string) error {
	options, err := configurer.ListConfigOptions(ctx)
	if err != nil {
		return err
	}
	valid := func(options []ports.ChatConfigOption, requireCurrent bool) bool {
		modelOK, effortOK := false, false
		for _, option := range options {
			if option.ID == "model" {
				modelOK = option.Current.Select == model
			}
			if option.ID == "effort" && option.Type == ports.ChatConfigOptionSelect {
				for _, choice := range option.Choices {
					if choice.Value == effort {
						effortOK = !requireCurrent || option.Current.Select == effort
					}
				}
			}
		}
		return modelOK && effortOK
	}
	if !valid(options, false) {
		return fmt.Errorf("controlled OpenCode model/effort not supported: effort=%s", effort)
	}
	if valid(options, true) {
		return nil
	}
	if _, err := configurer.SetConfigOption(ctx, "effort", ports.ChatConfigOptionValue{Select: effort}); err != nil {
		return err
	}
	// Read the provider again: accepting a setter is not evidence it took effect.
	options, err = configurer.ListConfigOptions(ctx)
	if err != nil {
		return err
	}
	if !valid(options, true) {
		return fmt.Errorf("controlled OpenCode effort did not become %s", effort)
	}
	return nil
}
