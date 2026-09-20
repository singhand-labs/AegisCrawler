# 更新日志

本项目所有显著变更都会记录在此文件中。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，并遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### Changed

- `docs/` 目录暂时不再随仓库发布（本地完整保留，`.gitignore` 忽略）；README 所需的生产部署要点、健康检查与本地测试命令已内联进双语 README。

## [0.2.0] - 2026-09-20

### Added

- **录制 → 需求 → DSL 工作流**：扩展录制后在意图确认向导中确认采集目的、规范化结构化需求、预览临时 DSL、完整浏览器回放验证、逐步人工确认后保存为不可变规则版本（`/api/v1/recordings`、`/api/v1/requirements`、`/api/v1/dsl-workflows`、`/api/v1/dsl-replays`）。
- 服务端 CLI 子命令：`serve` / `version` / `validate-config` / `migrate`。
- `config.yml` 配置文件层（环境变量 > config.yml > 默认值，解析失败拒绝启动）。
- LLM `enforced` 策略模式：多 provider route、价格与硬预算（`WORKFLOW_BUDGET_USD_CENTS`）、fit gates、自动尝试上限与 `LLM_CALL_MAX_RETRIES` 重试开关。
- 严格输出 schema 体系：`$defs` 化 extract 目标、`extractPageInfo` 后置导航输出、分支原因诊断，提升 DSL 生成一次通过率。
- LLM 抽取锚定在录制实际演示的 DOM 区域，减少幻觉选择器。
- Admin UI：规则详情页 DAG 流程图（@antv/g6）与结构化列表双视图、单次/定时任务、调度计划与审计日志管理。
- 浏览器扩展 AI 增强入口：baseline 规则经 LLM patch、安全扫描后生成 pending 规则，管理后台 diff 审核应用/拒绝。
- 仓库治理文件：`LICENSE`（GPL-3.0-or-later 全文）、`CONTRIBUTING.md`、`CODE_OF_CONDUCT.md`、`SECURITY.md`、`CHANGELOG.md`、`.editorconfig`、Issue/PR 模板、dependabot 配置。

### Changed

- Go module 路径从 `github.com/opencrawler/page-agent-service` 迁移至 `github.com/singhand-labs/AegisCrawler`；用户可见品牌字符串从 `OpenCrawler` 统一为 `AegisCrawler`。
- README 双语化：英文主文档 + `README.zh-CN.md` 中文完整参考；新增端到端演示 GIF 与社交预览资产。
- 发布流水线恢复：版本 tag 自动构建多平台 Docker 镜像（GHCR）并发布浏览器扩展 zip 与 userscript。
- `.gitignore` 追加 `.env`、`.env.local`、`.idea/`、`.vscode/`、`*.swp`、`.DS_Store` 等。

### Fixed

- ScriptCat userscript 执行器可部署性与自清理：会话结束自动收尾，输出符合 attempt 级 worker 协议。
- 无行数据的成功完成不再判定为回放失败。
- 录制快照超大分块有界化，STOP 阶段不再可能挂起。
- Anthropic 适配器兼容 reasoning-first 网关。
- 采集需求确认幂等化；超长录制分块在生成派发前裁剪。
- enforced 工作流 fit gates 由策略路由窗口推导；默认上报速率预算覆盖单次抽取突发。
- 跨源键盘导航不再从录制中丢失。

### Removed

- 移除内部 SDD 流程目录 `.superpowers/`（含 Windows 开发机本地路径泄露）与内部研发计划目录 `docs/superpowers/`。

## [0.1.0] - 2026-07-15

### Added

- **PageResearch Agent 浏览器扩展**：在真实页面录制点击、输入、滚动、拖拽等操作，自动生成可执行的 DSL 规则。
- **Go 服务端**：规则 / 任务 / 结果 / 日志 / 审计的统一真相源，提供 OpenAPI/Swagger 接口。
  - 基于 lease 的任务认领、心跳续租、sweeper 自动回收与重试、死信队列。
  - 请求体限制、全局超时、熔断、分级限流（全局 / worker / 站点）。
  - SQLite WAL + 版本化迁移（纯 Go，无需 CGO）。
- **ScriptCat Worker**：在浏览器端稳定执行 DSL 规则，采集数据并回传服务端。
- **Admin UI 管理后台**：React + Ant Design 5 中文可视化界面，支持规则 CRUD、单次/定时任务、调度计划、审计日志；规则详情页提供 DAG 流程图 + 结构化列表视图。
- **LLM 规则增强（可选）**：对接 OpenAI / Anthropic 等兼容 API，对录制 baseline 规则进行 patch 增强；支持 diff 审核、一键应用/拒绝、结果缓存与反思校验。
- **DSL 全场景动作**：click、type、scroll、drag、slide、upload、hover、wait、evaluate、loop、if、switch、parallel、extract。
- **任务取消**：Admin UI/API 标记取消后，Worker 通过心跳响应立即中止正在执行的规则。
- **可观测性**：结构化日志（zap）、Prometheus 指标、健康检查（含数据库 readiness）、DB size 指标。
- **CI/CD 发布**：版本 tag 自动构建多平台 Docker 镜像推送 GHCR，并发布浏览器扩展 zip 与 userscript。
- **11 个 DSL 规则示例**，覆盖电商、表单、登录、流水线、网络并行等典型场景。
- **完整文档**：README、`docs/deployment.md`、`docs/operations.md`、`docs/security.md`、`docs/dsl-design.md`、`docs/deployment-smoke-test.md`。
