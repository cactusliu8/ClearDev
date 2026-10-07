package devhandoff

import (
	"fmt"
	"regexp"
	"strings"
)

// Check modes for stage handoff documents.
const (
	ModePlan      = "plan"
	ModeCandidate = "candidate"
	ModeAccept    = "accept"
	ModeClose     = "close"
	ModeAuto      = "auto"
)

type stageDocs struct {
	ID           string
	Plan         string
	Execution    string
	Acceptance   string
	Roadmap      string
	PlanExists   bool
	ExecExists   bool
	AcceptExists bool
}

type parsedPlan struct {
	Version      string
	Status       string
	StartSHA     string
	CandidateSHA string
}

type parsedExecution struct {
	Status        string
	PlanVersion   string
	StartSHA      string
	PlanCommitSHA string
	CandidateSHA  string
}

type parsedAcceptance struct {
	Conclusion   string
	PlanVersion  string
	StartSHA     string
	CandidateSHA string
	ExecutionSHA string
}

type parsedRoadmap struct {
	Stage        string
	Status       string
	Version      string
	PlanLink     string
	CurrentBlock string
}

var (
	shaRe          = regexp.MustCompile("`([0-9a-f]{40})`")
	versionRe      = regexp.MustCompile(`v\d+`)
	planLinkRe     = regexp.MustCompile(`stages/(S\d+[A-Z0-9]*)/plan\.md`)
	conclusionRe   = regexp.MustCompile(`验收结论[：:]\s*([^\s。，]+)`)
	statusLineRe   = regexp.MustCompile(`当前状态[：:]\s*(\S+)`)
	roadmapStatRe  = regexp.MustCompile(`(?m)^-\s*状态[：:]\s*(.+)$`)
	roadmapStageRe = regexp.MustCompile(`(?m)^-\s*阶段[：:]\s*(S\d+[A-Z0-9]*)`)
)

func parsePlan(content string) parsedPlan {
	return parsedPlan{
		Version:      firstVersion(content, "计划版本"),
		Status:       firstStatus(content),
		StartSHA:     labeledSHA(content, "起始 Git"),
		CandidateSHA: labeledSHA(content, "候选提交"),
	}
}

func parseExecution(content string) parsedExecution {
	return parsedExecution{
		Status:        firstStatus(content),
		PlanVersion:   firstVersion(content, "计划版本"),
		StartSHA:      labeledSHA(content, "计划起始"),
		PlanCommitSHA: labeledSHA(content, "计划提交"),
		CandidateSHA:  labeledSHA(content, "候选 Git 提交"),
	}
}

func parseAcceptance(content string) parsedAcceptance {
	conclusion := ""
	if match := conclusionRe.FindStringSubmatch(content); match != nil {
		conclusion = strings.Trim(match[1], "“”\"")
	}
	candidate := labeledSHA(content, "本轮候选")
	if candidate == "" {
		candidate = labeledSHA(content, "候选提交")
	}
	return parsedAcceptance{
		Conclusion:   conclusion,
		PlanVersion:  firstVersion(content, "计划版本"),
		StartSHA:     labeledSHA(content, "计划起始"),
		CandidateSHA: candidate,
		ExecutionSHA: labeledSHA(content, "开发记录提交"),
	}
}

func parseRoadmap(content string) parsedRoadmap {
	current := currentStageSection(content)
	status := ""
	if match := roadmapStatRe.FindStringSubmatch(current); match != nil {
		status = strings.TrimSpace(match[1])
	}
	stage := ""
	if match := roadmapStageRe.FindStringSubmatch(current); match != nil {
		stage = match[1]
	}
	link := ""
	if match := planLinkRe.FindStringSubmatch(current); match != nil {
		link = match[0]
		if stage == "" {
			stage = match[1]
		}
	}
	version := ""
	if match := versionRe.FindString(status); match != "" {
		version = match
	}
	return parsedRoadmap{Stage: stage, Status: status, Version: version, PlanLink: link, CurrentBlock: current}
}

func currentStageSection(content string) string {
	start := strings.Index(content, "## 当前阶段")
	if start < 0 {
		return content
	}
	rest := content[start:]
	if next := strings.Index(rest, "\n## "); next > 0 {
		return rest[:next]
	}
	return rest
}

func firstStatus(content string) string {
	if match := statusLineRe.FindStringSubmatch(content); match != nil {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func firstVersion(content, label string) string {
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, label) {
			continue
		}
		if match := versionRe.FindString(line); match != "" {
			return match
		}
	}
	return ""
}

func labeledSHA(content, label string) string {
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, label) {
			continue
		}
		if match := shaRe.FindStringSubmatch(line); match != nil {
			return match[1]
		}
	}
	return ""
}

func detectMode(plan parsedPlan, execExists bool, execution parsedExecution, acceptExists bool) string {
	if plan.Status == "已完成" {
		return ModeClose
	}
	if acceptExists {
		return ModeAccept
	}
	if execExists && execution.Status == "待验收" {
		return ModeCandidate
	}
	return ModePlan
}

func allowedConclusion(value string) bool {
	switch value {
	case "通过", "需要返工", "计划需要修改", "阻塞":
		return true
	default:
		return false
	}
}

func statusToken(value string) string {
	value = strings.TrimSpace(value)
	if i := strings.Index(value, "（"); i > 0 {
		return value[:i]
	}
	if i := strings.Index(value, "("); i > 0 {
		return value[:i]
	}
	return value
}

func fmtErrs(errs []string) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(errs, "\n"))
}
