package cleardev

import "strings"

// Shared by new Steward, Planner and Builder messages. This describes current
// capabilities; it does not grant paths or convert unsupported projects to npm.
const projectEnvironmentGuidance = `
Project environment constraints / 按项目适用的工程约束：
先调查实际项目语言、包管理器、依赖清单、锁文件、构建布局和运行形式；空项目先提出适合目标的技术方案。通用范围、证据、审核和恢复规则不等于技术栈规则，不能把所有软件当成npm项目。
当前可执行环境只有NODE_NPM_V1（仓库根目录单包Node/npm）。Python、Go、Rust、其他包管理器、多包或特殊平台保留其真实需求；缺执行能力时明确NEEDS_CAPABILITY，不伪装成npm，不要求用户为平台迁就改写项目。历史runtime缺省只用于兼容，不是项目类型调查结论。
选择Node/npm且声明依赖时，阶段writePaths与检查mainPaths应包含package.json和package-lock.json；修改依赖的任务也须同时获得两文件权限及对应requiredChecks覆盖。只消费已有依赖的后续源码任务不必取得这两文件写权限。无外部依赖的Node项目不强制锁文件；其他技术栈不套用npm锁文件。
Steward把适用约束写入executionBasis；Planner核对实际基线与任务前置条件，把约束落实到任务范围和检查。能力说明不授予新路径，不能靠写一句“需锁文件”覆盖缺失的writePaths。
Builder核对执行包与实际依赖是否一致；发现必要文件不在范围内，应报告准确路径和约定矛盾并使用现有coordination，不能删除必要锁文件、忽略依赖或声称CANDIDATE_READY来绕过。Planner协调仍受现有范围约束，不能承诺可以自动扩大已批准路径。
只有需要常驻服务的项目才要求服务启动和HTTP健康/浏览器试用；CLI或库用符合功能的命令试用。需要持久数据时采用约定数据目录，无持久数据的项目不要求数据库。
`

func projectBuilderEnvironmentDependencies() string {
	return strings.ReplaceAll(projectBuilderReliableDependencies,
		"提交完整锁文件；", "存在外部依赖或workspace时提交完整锁文件；无外部依赖且无workspace的Node项目可省略锁文件；")
}
