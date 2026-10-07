package cleardevdemo

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// Options configure a demonstration run. Callers cannot choose mode, tasks,
// paths, checks, sessions, candidates, or completion state.
type Options struct {
	Stdout        io.Writer
	Stderr        io.Writer
	Now           func() time.Time
	LookPath      func(string) (string, error)
	CommandOutput func(ctx context.Context, name string, args ...string) ([]byte, error)
	ElectronBin   string
	RepoRoot      string
	ParentDir     string
	// ForcedRoot is for tests. Production runs always allocate a new tree.
	ForcedRoot string
	// HostDisplay is the real desktop where the VNC viewer must be visible.
	// Production uses DISPLAY. Tests may provide an explicit value.
	HostDisplay string
}

func (o Options) stdout() io.Writer {
	if o.Stdout != nil {
		return o.Stdout
	}
	return os.Stdout
}

func (o Options) stderr() io.Writer {
	if o.Stderr != nil {
		return o.Stderr
	}
	return os.Stderr
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now().UTC()
}

// Layout is one isolated demonstration directory tree. A second run always
// allocates a new tree and never overwrites an existing evidence directory.
type Layout struct {
	Root        string
	HomeDir     string
	DataDir     string
	RunFile     string
	RepoDir     string
	EvidenceDir string
	AppDir      string
	StartupFile string
	VNCAuthFile string
	Port        int
	DebugPort   int
	AppPort     int
	VNCPort     int
}

// BuildSource is embedded in the packaged app before either demonstration run.
type BuildSource struct {
	SchemaVersion   int      `json:"schemaVersion"`
	CandidateCommit string   `json:"candidateCommit"`
	SourceClean     bool     `json:"sourceClean"`
	SourceScope     []string `json:"sourceScope"`
	BuiltAt         string   `json:"builtAt"`
}

// VisibilityEvidence proves that a password-protected loopback VNC server was
// opened in a viewer window on the user's current desktop.
type VisibilityEvidence struct {
	SchemaVersion      int    `json:"schemaVersion"`
	HostDisplay        string `json:"hostDisplay"`
	VNCAddress         string `json:"vncAddress"`
	PasswordProtected  bool   `json:"passwordProtected"`
	ViewerExecutable   string `json:"viewerExecutable"`
	ViewerWindowWidth  int    `json:"viewerWindowWidth"`
	ViewerWindowHeight int    `json:"viewerWindowHeight"`
	ScreenshotSource   string `json:"screenshotSource"`
	ScreenshotSHA256   string `json:"screenshotSha256"`
}

// CleanupEvidence records the post-run checks performed after every managed
// process has been stopped and every non-evidence work directory removed.
type CleanupEvidence struct {
	SchemaVersion       int    `json:"schemaVersion"`
	ProcessesStopped    bool   `json:"processesStopped"`
	PortsClosed         bool   `json:"portsClosed"`
	WorkDirectoriesGone bool   `json:"workDirectoriesGone"`
	CheckedPorts        []int  `json:"checkedPorts"`
	FailureReason       string `json:"failureReason,omitempty"`
}

// Evidence is the offline-checkable demonstration pack. It stores identifiers
// and hashes, not tokens, auth files, or full host home paths.
type Evidence struct {
	SchemaVersion        int               `json:"schemaVersion"`
	StartedAt            string            `json:"startedAt"`
	FinishedAt           string            `json:"finishedAt"`
	DurationMS           int64             `json:"durationMs"`
	InputSHA256          map[string]string `json:"inputSha256"`
	RequirementID        string            `json:"requirementId,omitempty"`
	RequirementVersionID string            `json:"requirementVersionId,omitempty"`
	RequirementSHA256    string            `json:"requirementSha256,omitempty"`
	PlanID               string            `json:"planId,omitempty"`
	PlanSHA256           string            `json:"planSha256,omitempty"`
	PlanReviewID         string            `json:"planReviewID,omitempty"`
	PlanReviewVerdict    string            `json:"planReviewVerdict,omitempty"`
	TaskSetVersion       int64             `json:"taskSetVersion,omitempty"`
	Mode                 string            `json:"mode,omitempty"`
	ModeReason           string            `json:"modeReason,omitempty"`
	FixedBuilderCount    int               `json:"fixedBuilderCount,omitempty"`
	TemplateCommit       string            `json:"templateCommit,omitempty"`
	IntegrationSHA       string            `json:"integrationCommitSha,omitempty"`
	IntegrationCheckIDs  []string          `json:"integrationCheckIds,omitempty"`
	CheckImageID         string            `json:"checkImageId,omitempty"`
	RoleSessions         map[string]string `json:"roleSessions,omitempty"`
	Tasks                []map[string]any  `json:"tasks,omitempty"`
	SpecialistChecks     []map[string]any  `json:"specialistChecks,omitempty"`
	CheckRuns            []map[string]any  `json:"checkRuns,omitempty"`
	Reviews              []map[string]any  `json:"reviews,omitempty"`
	ProgressPhase        string            `json:"progressPhase,omitempty"`
	ExplanationSHA256    string            `json:"explanationSha256,omitempty"`
	Sample               map[string]int    `json:"sample,omitempty"`
	MaxEventSequence     int64             `json:"maxEventSequence,omitempty"`
	Usage                map[string]any    `json:"usage,omitempty"`
	BuildCandidateCommit string            `json:"buildCandidateCommit,omitempty"`
	BuildSourceClean     bool              `json:"buildSourceClean,omitempty"`
	BuildSourceSHA256    string            `json:"buildSourceSha256,omitempty"`
	RuntimeSHA256        map[string]string `json:"runtimeSha256,omitempty"`
	StartupSHA256        string            `json:"startupSha256,omitempty"`
	VisibilitySHA256     string            `json:"visibilitySha256,omitempty"`
	CleanupSHA256        string            `json:"cleanupSha256,omitempty"`
}

func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o600)
}

func writeExactFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o600)
}

func sha256File(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // evidence and template files are local demonstration inputs
	if err != nil {
		return "", err
	}
	return sha256String(string(raw)), nil
}

func sha256String(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum)
}
