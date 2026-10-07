package cleardev

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// Opt-in UI fixture only: simulated completed discovery, no requirement approval,
// execution admission, Builder, model call, or stage acceptance is manufactured.
func TestStageTrialVisualFixture(t *testing.T) {
	destination := os.Getenv("CLEARDEV_STAGE_TRIAL_VISUAL_DATA")
	if destination == "" {
		t.Skip("opt-in isolated UI fixture")
	}
	f := newProjectPlanningFixture(t, "EXISTING")
	product := f.create(t)
	var reply core.ProductDiscoveryResult
	_ = json.Unmarshal([]byte(genericProjectReply("existing")), &reply)
	stage := &reply.Stages[0]
	stage.Title = "命令与文件试用（界面测试数据）"
	stage.Goal = "验证阶段试用合同的显示；不代表模型或功能已验收。"
	stage.AcceptanceCriteria = []string{"默认样例的三行任务统计正确。"}
	stage.ExecutionBasis.Launch = core.ProjectLaunch{Argv: []string{}, WorkingDirectory: ".", Description: "一次性 CLI，运行结束后核对输出及 report.txt。"}
	stage.ExecutionBasis.Trial = &core.ProjectTrial{SchemaVersion: 1, Steps: []core.ProjectTrialStep{{ID: "sample", Kind: "COMMAND", Argv: []string{"node", "src/taskstat.mjs"}, TimeoutSeconds: 30, AcceptanceCriteria: stage.AcceptanceCriteria, Observe: "逐项核对 total/done/overdue", OutputFiles: []string{"report.txt"}}}}
	raw, _ := json.Marshal(reply)
	f.h.replies = append(f.h.replies, string(raw))
	if _, err := f.s.SubmitProductDiscussion(context.Background(), product.Goal.ID, projectChoice(product, "existing")); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.OpenFile(filepath.Join(destination, "ao.db"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
}
