package cleardev

import (
	"reflect"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// ValidateControlledConfiguration must succeed before accepting controlled work
// or recovering it at daemon startup. Pure fact operations retain their own gates.
func (s *Service) ValidateControlledConfiguration() error {
	required := []struct {
		name  string
		value any
	}{
		{"Facts", s.facts}, {"StandardFacts", s.standard}, {"ComplexFacts", s.complex},
		{"ComplexExecutionFacts", s.complexExecution}, {"DirectionFacts", s.direction},
		{"HumanDecisions", s.humanDecisions}, {"ProgressExplanations", s.progress},
		{"ParseCorrections", s.corrections}, {"AgentAttempts", s.attempts},
		{"ControlledPreflights", s.preflights}, {"ControlledPreflightChecker", s.preflightChecker},
		{"AO", s.ao}, {"Workspace", s.workspace}, {"Sessions", s.sessions},
		{"RecoverAgentSession", s.recoverAgentSession}, {"Chat", s.chat},
		{"Inspector", s.inspector}, {"Checks", s.checks},
	}
	if s.benchmarkManifest != nil {
		required = append(required, struct {
			name  string
			value any
		}{"BenchmarkFacts", s.benchmarkFacts})
	}
	var missing []string
	for _, dependency := range required {
		if missingControlledDependency(dependency.value) {
			missing = append(missing, dependency.name)
		}
	}
	if len(missing) != 0 {
		return apierr.Internal("CLEARDEV_CONFIGURATION_INVALID", "Missing ClearDev controlled dependencies: "+strings.Join(missing, ", "))
	}
	return nil
}

func missingControlledDependency(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
