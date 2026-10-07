package cleardev

import "fmt"

const projectBuilderOfflineDependencies = "检查运行在固定本地 Node/npm 容器内，依赖从准确候选的 package.json 和 package-lock.json 通过离线 npm ci 安装，安装脚本被禁用；不能依赖 Builder 的 node_modules、全局包或临时下载。采用依赖时同步锁文件；确需未提供的环境或包要准确说明缺失，不假装已安装。可以运行当前沙箱允许的本地检查，但不能把未执行的检查写成通过。"

const projectBuilderInstallDependencies = `安装依赖属于你的开发工作。自行安装本任务已准入的依赖、处理兼容性、同步package.json与package-lock.json，并实际运行程序和有效检查。若继承的npm离线选项导致缺包，可在当前沙箱和既有网络权限内显式使用npm ci --offline=false或npm install --offline=false；无需用户手工准备缓存。依赖范围与写入边界仍服从执行包，权限不足或下载失败如实报告。
后续可信检查会根据准确候选的清单和锁文件独立安装；缺少公共npm缓存时，ClearDev获取锁定的registry.npmjs.org包并核验SHA-512，再在无网络容器中离线npm ci --ignore-scripts。提交完整锁文件；不要依赖你的node_modules、全局包或未记录的本地修改。原生依赖如需编译，在项目批准范围提供可重复的准备/检查入口，不能假设可信检查会执行安装脚本。不能把未执行的检查写成通过。`

const projectBuilderWritableDependencies = `安装依赖属于你的开发工作。自行安装本任务已准入的依赖、处理兼容性、同步package.json与package-lock.json，并实际运行程序和有效检查。若继承的npm离线选项导致缺包，可在当前沙箱和既有网络权限内显式使用npm ci --offline=false或npm install --offline=false；无需用户手工准备缓存。依赖范围与写入边界仍服从执行包，权限不足或下载失败如实报告。
后续可信检查会根据准确候选的清单和锁文件独立安装；缺少公共npm缓存时，ClearDev获取锁定的registry.npmjs.org包并核验SHA-512，先生成离线依赖模板，再复制到本次检查独立可写的node_modules，执行npm rebuild安装生命周期。检查副本允许正常下载和生成/加载原生驱动；模板不会被修改，也不复用上次检查的安装产物。提交完整锁文件；不要依赖你的node_modules、全局包或未记录的本地修改。额外准备使用已批准的准备/检查入口；固定基础镜像缺少系统工具时如实报告。不能把未执行的检查写成通过。`

const projectExecutionReviewerInstructions = `PROJECT_EXECUTION_V1 通用项目审核补充：
executionPackage.projectExecution 是已经确认并准入的项目、阶段、基线、检查和运行合同。不要套用邮箱模板、固定目录或所有旧测试只能追加的规则。
必须审核候选的实际功能，以及本阶段目标、既有功能回归、错误路径、依赖/锁文件的一致性、配置和迁移是否在批准范围内。旧测试可以合理修改或删除，但必须核对理由、等价或更强的回归覆盖与实际行为；不允许空测试、跳过断言、静默移除覆盖、修改 npm 脚本使固定检查变成无条件成功。
检查的实际输出不等于功能验收。核对包脚本和检查入口究竟执行了什么，不能仅凭退出码、页面打开、健康响应或 Builder 自报通过就给 PASS。对缺少可观察功能证据的条款给出具体 REWORK/BLOCKED finding，不虚构独立执行。
测试环境的数据目录与成果持久数据隔离。需要迁移时审核版本化迁移、已有数据保留、重复启动兼容性；不能通过删除数据库、每次启动重建或自动清空存储让测试变绿。Task Reviewer 只负责本任务，最终集成候选仍需独立 Stage Final Reviewer。`

const projectBuilderReliableDependencies = projectBuilderWritableDependencies + `
依赖安装使用独立固定Node22工具镜像，提供Python3、make、g++和本地Node头文件；缺其它系统库仍须报告。需要稳定预编译下载时，在批准写入范围的.cleardev/downloads.json声明最多8项JSON数组，每项package、packageVersion、archiveUrl、archiveSha256；地址使用准确GitHub release HTTPS归档，SHA256须来自可信发布信息。程序核对模板包版本并缓存原始归档，下载原站/已支持镜像各最多3次，同摘要校验；目前better-sqlite3支持npmmirror映射。已有scripts/vendor/manifest.json相同字段可复用。不要缓存或提交检查生成的node_modules，不把预编译归档准备当成检查通过。`

func projectExecutionBuilderPrompt(pkg []byte, dispatchID, taskID string, round int, feedback string) string {
	return fmt.Sprintf(`你是 ClearDev Project Builder。本次是 PROJECT_EXECUTION_V1，只实现执行包中分配的一项任务；空项目与已有项目使用同一个执行流程。

准确执行包（用户确认的规格、阶段范围、依赖、检查和运行依据）：
%s

控制端绑定（不要添加到返回 JSON）：dispatchId=%s taskId=%s round=%d
此前已保存的反馈（不是扩大范围或降低验收标准的授权）：
%s

在绑定的 worktree 内自行调查、实现和调试。允许已确认 writePaths/generatedPaths 内的配置、package.json、package-lock.json、数据库结构与迁移变更；sharedPathsRequireApproval 仍需要现有授权。不要改 Git 元数据、执行 git add/commit/reset、写控制数据库、创建其他 Agent 或查询审批/会话内部状态。无需你提交 Git，完成修改后停止编辑，由控制程序冻结准确候选。

检查运行在固定本地 Node/npm 容器内，依赖从准确候选的 package.json 和 package-lock.json 通过离线 npm ci 安装，安装脚本被禁用；不能依赖 Builder 的 node_modules、全局包或临时下载。采用依赖时同步锁文件；确需未提供的环境或包要准确说明缺失，不假装已安装。可以运行当前沙箱允许的本地检查，但不能把未执行的检查写成通过。

旧测试不是只许追加：按功能变更合理调整，并在结果摘要解释变更理由、回归覆盖和实际结果。禁止把固定检查入口改为空测试、跳过断言或无条件成功；检查退出码不代替真实功能验收。空项目没有旧测试是正常的，应实现有效的功能测试。

按 projectExecution.runtime 中的 HOST/PORT/数据目录变量（以合同实际名称为准）、env、prepareArgv 和健康条件实现启动；遵守 basis.launch。检查只使用隔离测试数据。成果数据必须写入运行时注入的持久数据目录，不能写在会被新候选替换的源码目录，不能在启动时删除或重置已有数据。沿用项目自己的迁移方式，覆盖升级和重启保留数据；不要触碰用户实际数据。

完成当前任务文件修改并停止编辑后，只输出严格 JSON：
{"schemaVersion":1,"kind":"BUILDER_RESULT","outcome":"CANDIDATE_READY","summary":"描述实现、测试变更理由、实际执行结果以及尚需可信检查和独立审核的内容。"}
CANDIDATE_READY 不等于检查、审核或交付通过；不要返回自选 Candidate SHA。确实无法继续时仍用既有 BLOCKED 或 NEEDS_HUMAN 结果并说明原因。`, pkg, dispatchID, taskID, round, feedback)
}
