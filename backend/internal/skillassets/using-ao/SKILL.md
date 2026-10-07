---
name: using-ao
description: ClearDev 公开快照中的兼容运行时说明。原 Agent Orchestrator 操作文档未随公开仓库发布。
trigger: 当运行时仍通过兼容路径引用 using-ao 技能时使用。
---

# ClearDev 兼容运行时说明

当前公开仓库保留了部分 Agent Orchestrator 兼容代码，因此运行时仍可能引用 `using-ao` 这个历史技能目录。

这里不包含原项目的完整操作手册、开发流程或内部运行说明。当前阶段请以程序自身提供的命令帮助、接口返回和 ClearDev 的显式任务要求为准。

- 不要根据本文件推断未公开的开发流程。
- 不要自动创建发布、部署或远程操作。
- 涉及浏览器或预览时，只执行当前任务明确要求的动作。
- 如果某项旧 AO 工作流依赖这里缺失的说明，应视为当前公开快照尚未支持。

兼容占位文件：

- [浏览器说明](commands/browser.md)
- [预览说明](commands/preview.md)
- [会话启动说明](commands/spawn.md)
