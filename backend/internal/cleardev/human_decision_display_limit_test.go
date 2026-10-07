package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

// The human-decision window shows exactly what the model produced, so the
// display bound must match the model output bound: text between the old 2000
// and the new 10000 characters reaches the native window. The whole agent
// result still has to stay inside maxAgentResultBytes (32 KiB).
func TestHumanDecisionDisplayAcceptsLongModelText(t *testing.T) {
	long := strings.Repeat("逾期只算未完成且 due 早于今天。", 300) // 约 5,100 字 ≈ 15 KiB
	if runes := len([]rune(long)); runes <= 2000 || runes > 10000 {
		t.Fatalf("测试文本长度不在预期区间: %d", runes)
	}
	raw, err := json.Marshal(map[string]string{
		"title": "确认需求版本", "summary": long, "fullContent": "见原始内容", "changeSummary": "首次确认",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxAgentResultBytes {
		t.Fatalf("测试结果超过原始上限: %d", len(raw))
	}
	if _, err := ParseHumanDecisionDisplay(raw); err != nil {
		t.Fatalf("超过 2000 但在 10000 内的展示文本被拒绝: %v", err)
	}
	tooLong, err := json.Marshal(map[string]string{
		"title": "确认需求版本", "summary": strings.Repeat("超", 10001), "fullContent": "x", "changeSummary": "y",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseHumanDecisionDisplay(tooLong); err == nil {
		t.Fatal("超过上限的展示文本被接受")
	}
}
