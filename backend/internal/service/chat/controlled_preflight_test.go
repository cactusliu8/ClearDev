package chat_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

type inspectingDriver struct {
	fakeDriver
	inspect func() (ports.ChatAccountInspection, error)
}

func (d inspectingDriver) InspectAccount(context.Context) (ports.ChatAccountInspection, error) {
	if d.inspect == nil {
		return ports.ChatAccountInspection{}, ports.ErrChatProviderUnavailable
	}
	return d.inspect()
}

func liveCatalog(ids ...string) []ports.ChatModel {
	models := make([]ports.ChatModel, 0, len(ids))
	for i, id := range ids {
		models = append(models, ports.ChatModel{ID: id, Default: i == 0})
	}
	return models
}

func TestCheckControlledPreflightRejectsModelMissingFromLiveCatalog(t *testing.T) {
	svc := chatsvc.New(chatsvc.Options{
		Drivers: fakeRegistry{driver: inspectingDriver{
			inspect: func() (ports.ChatAccountInspection, error) {
				return ports.ChatAccountInspection{Models: liveCatalog("gpt-5.4", "gpt-5.3-codex")}, nil
			},
		}},
	})
	got, err := svc.CheckControlledPreflight(context.Background(), domain.HarnessCodex, "gpt-5.6-sol")
	if !errors.Is(err, ports.ErrChatModelNotAvailable) {
		t.Fatalf("err = %v, want ErrChatModelNotAvailable", err)
	}
	if got.CatalogJSON == "" || got.CatalogSHA256 == "" || !strings.Contains(got.CatalogJSON, "gpt-5.4") {
		t.Fatalf("catalog was not recorded: %+v", got)
	}
	if strings.Contains(got.ErrorSummary, "不存在") || strings.Contains(got.ErrorSummary, "gpt-5.6-sol") && strings.Contains(got.ErrorSummary, "模型") {
		t.Fatalf("classified from localized text: %q", got.ErrorSummary)
	}
}

func TestCheckControlledPreflightDoesNotParseLocalizedProviderText(t *testing.T) {
	svc := chatsvc.New(chatsvc.Options{
		Drivers: fakeRegistry{driver: inspectingDriver{
			inspect: func() (ports.ChatAccountInspection, error) {
				return ports.ChatAccountInspection{}, fmt.Errorf("%w: 模型代码：不存在", ports.ErrChatAuthRequired)
			},
		}},
	})
	got, err := svc.CheckControlledPreflight(context.Background(), domain.HarnessCodex, "gpt-5.4")
	if !errors.Is(err, ports.ErrChatAuthRequired) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(got.ErrorSummary, "模型") || strings.Contains(got.ErrorSummary, "不存在") {
		t.Fatalf("copied localized text: %q", got.ErrorSummary)
	}
}

func TestCheckControlledPreflightRecordsProviderDefaultWhenUnset(t *testing.T) {
	svc := chatsvc.New(chatsvc.Options{
		Drivers: fakeRegistry{driver: inspectingDriver{
			inspect: func() (ports.ChatAccountInspection, error) {
				return ports.ChatAccountInspection{Models: liveCatalog("gpt-5.4", "gpt-5.3-codex")}, nil
			},
		}},
	})
	got, err := svc.CheckControlledPreflight(context.Background(), domain.HarnessCodex, "")
	if err != nil {
		t.Fatalf("CheckControlledPreflight: %v", err)
	}
	if got.ResolvedModel != "gpt-5.4" {
		t.Fatalf("resolved = %q", got.ResolvedModel)
	}
}

func TestCheckControlledPreflightQuotaExhaustedUsesRetryAt(t *testing.T) {
	fixed := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	svc := chatsvc.New(chatsvc.Options{
		Now: func() time.Time { return fixed },
		Drivers: fakeRegistry{driver: inspectingDriver{
			inspect: func() (ports.ChatAccountInspection, error) {
				return ports.ChatAccountInspection{
					Models: liveCatalog("gpt-5.4"),
					RateLimits: ports.ChatRateLimits{
						QuotaExhausted:           true,
						SecondaryResetsInSeconds: 3600,
						ProviderErrorCode:        "workspace_owner_credits_depleted",
					},
				}, nil
			},
		}},
	})
	got, err := svc.CheckControlledPreflight(context.Background(), domain.HarnessCodex, "gpt-5.4")
	if !errors.Is(err, ports.ErrChatQuotaExhausted) {
		t.Fatalf("err = %v", err)
	}
	if got.RetryAt == nil || !got.RetryAt.Equal(fixed.Add(time.Hour)) {
		t.Fatalf("retryAt = %v", got.RetryAt)
	}
	if got.ProviderErrorCode != "workspace_owner_credits_depleted" {
		t.Fatalf("provider code = %q", got.ProviderErrorCode)
	}
}

func TestCheckControlledPreflightDriverWithoutInspectorIsIncompatible(t *testing.T) {
	svc := chatsvc.New(chatsvc.Options{Drivers: fakeRegistry{driver: fakeDriver{}}})
	_, err := svc.CheckControlledPreflight(context.Background(), domain.HarnessCodex, "")
	if !errors.Is(err, ports.ErrChatDriverIncompatible) {
		t.Fatalf("err = %v, want ErrChatDriverIncompatible", err)
	}
}
