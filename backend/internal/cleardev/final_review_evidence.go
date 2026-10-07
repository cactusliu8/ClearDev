package cleardev

import (
	"encoding/json"
	"fmt"
)

// The saved packet and legacy prompts are unchanged. New prompts name the
// complete, hash-bound packet and index it, instead of recursively inlining it.
func finalReviewPromptPacket(review RequirementFinalReview) string {
	var packet RequirementFinalReviewPacket
	if json.Unmarshal([]byte(review.ReviewPacketJSON), &packet) != nil || packet.EvidenceFile == "" {
		return review.ReviewPacketJSON
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(review.ReviewPacketJSON), &fields) != nil {
		return review.ReviewPacketJSON
	}
	sizes := make(map[string]int, len(fields))
	for key, value := range fields {
		sizes[key] = len(value)
	}
	index := struct {
		File        string         `json:"evidenceFile"`
		SHA         string         `json:"packetSha256"`
		Bytes       int            `json:"packetBytes"`
		Fields      map[string]int `json:"fieldBytes"`
		Requirement string         `json:"requirement"`
	}{packet.EvidenceFile, review.ReviewPacketSHA256, len(review.ReviewPacketJSON), sizes, packet.Requirement.RequirementText}
	// Very large requirements remain in the complete packet too, not truncated.
	if len(index.Requirement) > 16000 {
		index.Requirement = "请按字段读取 evidenceFile 中完整 requirement；此处不截断或替代需求。"
	}
	raw, _ := json.Marshal(index)
	return fmt.Sprintf(`本消息仅交接当前候选、验收要求和证据索引。完整历史和审核材料只保留在下列同一份只读证据文件中，SHA-256 绑定完整原始字节。继续审核时复用该文件，不复制历史正文到消息或另建历史副本。索引不是证据的替代品。
先用只读命令核对文件 SHA-256，再用 Python/JSON 按字段和数组条目分段读取。不得一次 cat 整个文件或递归展开 plannerRuntime.requests 的 contextJson/prompt，否则会再次超过模型上下文。
必须读取 requirement 的全部 MUST/acceptance、plan、run 的当前工程合同、tasks 的有效执行包、当前候选的 checkRuns/taskReviews/verifications，以及 plannerRuntime 的全部决定/修订/恢复绑定。保留旧失败语义；按需要读取历史请求的原始上下文以核对来源。每个字段的完整原文都在文件中，不能把未读当作不存在，也不能以索引推断通过。
可用只读 JSON 命令选择单个字段/条目；这不是授权执行项目检查。遇到文件缺失、SHA 不符或无法读完必要事实，应 BLOCKED，不猜测。
%s`, raw)
}
