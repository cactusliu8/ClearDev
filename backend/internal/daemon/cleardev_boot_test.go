package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeClearDevBootRecovery struct {
	configurationErr                  error
	calls                             []string
	resumeErr                         error
	resumeComplexErr                  error
	resumeDirectionErr                error
	resumeComplexStandardExecutionErr error
	backfillErr                       error
	resumeProgressErr                 error
}

func (f *fakeClearDevBootRecovery) ResumeStandardFlows(context.Context) error {
	f.calls = append(f.calls, "resume")
	return f.resumeErr
}

func (f *fakeClearDevBootRecovery) ResumeComplexFlows(context.Context) error {
	f.calls = append(f.calls, "resume-complex")
	return f.resumeComplexErr
}

func (f *fakeClearDevBootRecovery) ResumeDirectionChanges(context.Context) error {
	f.calls = append(f.calls, "resume-direction")
	return f.resumeDirectionErr
}

func (f *fakeClearDevBootRecovery) ResumeComplexStandardExecutions(context.Context) error {
	f.calls = append(f.calls, "resume-complex-standard-execution")
	return f.resumeComplexStandardExecutionErr
}

func (f *fakeClearDevBootRecovery) BackfillHumanDecisionRequests(context.Context) error {
	f.calls = append(f.calls, "backfill")
	return f.backfillErr
}

func (f *fakeClearDevBootRecovery) ResumeProgressExplanations(context.Context) error {
	f.calls = append(f.calls, "resume-progress")
	return f.resumeProgressErr
}

func TestComplexParallelExecutionBootResumesBeforeHTTP(t *testing.T) {
	fake := &fakeClearDevBootRecovery{}
	if err := recoverClearDevOnBoot(context.Background(), fake); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 6 || fake.calls[3] != "resume-complex-standard-execution" {
		t.Fatalf("boot recovery calls = %v, want PARALLEL runs resumed by the existing complex execution recover path", fake.calls)
	}
}

func TestQuickExecutionBootResumesBeforeHTTP(t *testing.T) {
	fake := &fakeClearDevBootRecovery{}
	if err := recoverClearDevOnBoot(context.Background(), fake); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 6 || fake.calls[3] != "resume-complex-standard-execution" {
		t.Fatalf("boot recovery calls = %v, want QUICK runs resumed by the existing complex execution recover path", fake.calls)
	}
}

func TestControlledExceptionBootResumesBeforeHTTP(t *testing.T) {
	fake := &fakeClearDevBootRecovery{}
	if err := recoverClearDevOnBoot(context.Background(), fake); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 6 || fake.calls[3] != "resume-complex-standard-execution" {
		t.Fatalf("boot recovery calls = %v, want exception runs resumed by the existing complex execution recover path", fake.calls)
	}
}

func TestComplexStandardExecutionBootResumesBeforeHTTP(t *testing.T) {
	fake := &fakeClearDevBootRecovery{}
	if err := recoverClearDevOnBoot(context.Background(), fake); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 6 || fake.calls[2] != "resume-direction" || fake.calls[3] != "resume-complex-standard-execution" || fake.calls[4] != "backfill" || fake.calls[5] != "resume-progress" {
		t.Fatalf("boot recovery calls = %v, want direction then complex STANDARD execution resume before backfill/HTTP", fake.calls)
	}
}

func TestRecoverClearDevOnBootBackfillsWithoutDesktopChannel(t *testing.T) {
	fake := &fakeClearDevBootRecovery{}
	if err := recoverClearDevOnBoot(context.Background(), fake); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 6 || fake.calls[0] != "resume" || fake.calls[1] != "resume-complex" || fake.calls[2] != "resume-direction" || fake.calls[3] != "resume-complex-standard-execution" || fake.calls[4] != "backfill" || fake.calls[5] != "resume-progress" {
		t.Fatalf("boot recovery calls = %v, want resume, resume-complex, resume-direction, resume-complex-standard-execution, backfill, then resume-progress", fake.calls)
	}
}

func TestRecoverClearDevOnBootResumeFailureStopsBoot(t *testing.T) {
	fake := &fakeClearDevBootRecovery{resumeErr: errors.New("store closed")}
	err := recoverClearDevOnBoot(context.Background(), fake)
	if err == nil {
		t.Fatal("boot recovery succeeded")
	}
	if !strings.Contains(err.Error(), "resume ClearDev STANDARD flows on boot") {
		t.Fatalf("resume failure = %v", err)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "resume" {
		t.Fatalf("calls after resume failure = %v", fake.calls)
	}
}

func TestRecoverClearDevOnBootComplexResumeFailureStopsBoot(t *testing.T) {
	fake := &fakeClearDevBootRecovery{resumeComplexErr: errors.New("store closed")}
	err := recoverClearDevOnBoot(context.Background(), fake)
	if err == nil {
		t.Fatal("boot recovery succeeded")
	}
	if !strings.Contains(err.Error(), "resume ClearDev complex flows on boot") {
		t.Fatalf("complex resume failure = %v", err)
	}
	if len(fake.calls) != 2 || fake.calls[0] != "resume" || fake.calls[1] != "resume-complex" {
		t.Fatalf("calls after complex resume failure = %v", fake.calls)
	}
}

func TestRecoverClearDevOnBootComplexStandardExecutionFailureStopsBoot(t *testing.T) {
	fake := &fakeClearDevBootRecovery{resumeComplexStandardExecutionErr: errors.New("store closed")}
	err := recoverClearDevOnBoot(context.Background(), fake)
	if err == nil {
		t.Fatal("boot recovery succeeded")
	}
	if !strings.Contains(err.Error(), "resume ClearDev complex STANDARD executions on boot") {
		t.Fatalf("complex STANDARD execution resume failure = %v", err)
	}
	if len(fake.calls) != 4 || fake.calls[3] != "resume-complex-standard-execution" {
		t.Fatalf("calls after complex STANDARD execution failure = %v", fake.calls)
	}
}

func TestRecoverClearDevOnBootBackfillFailureStopsBoot(t *testing.T) {
	fake := &fakeClearDevBootRecovery{backfillErr: errors.New("store closed")}
	err := recoverClearDevOnBoot(context.Background(), fake)
	if err == nil {
		t.Fatal("boot recovery succeeded")
	}
	if !strings.Contains(err.Error(), "backfill ClearDev human decision requests on boot") {
		t.Fatalf("backfill failure = %v", err)
	}
	if len(fake.calls) != 5 || fake.calls[0] != "resume" || fake.calls[1] != "resume-complex" || fake.calls[2] != "resume-direction" || fake.calls[3] != "resume-complex-standard-execution" || fake.calls[4] != "backfill" {
		t.Fatalf("calls after backfill failure = %v", fake.calls)
	}
}

func TestRecoverClearDevOnBootProgressResumeFailureStopsBoot(t *testing.T) {
	fake := &fakeClearDevBootRecovery{resumeProgressErr: errors.New("store closed")}
	err := recoverClearDevOnBoot(context.Background(), fake)
	if err == nil {
		t.Fatal("boot recovery succeeded")
	}
	if !strings.Contains(err.Error(), "resume ClearDev progress explanations on boot") {
		t.Fatalf("progress resume failure = %v", err)
	}
	if len(fake.calls) != 6 || fake.calls[5] != "resume-progress" {
		t.Fatalf("calls after progress resume failure = %v", fake.calls)
	}
}

func (f *fakeClearDevBootRecovery) ValidateControlledConfiguration() error { return f.configurationErr }

func TestClearDevBootRejectsConfigurationBeforeRecovery(t *testing.T) {
	f := &fakeClearDevBootRecovery{configurationErr: errors.New("missing AgentAttempts")}
	if err := recoverClearDevOnBoot(context.Background(), f); err == nil || !strings.Contains(err.Error(), "AgentAttempts") {
		t.Fatalf("configuration error: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("recovery before configuration: %v", f.calls)
	}
}
