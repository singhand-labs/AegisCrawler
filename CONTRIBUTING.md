# 贡献指南

感谢你对 AegisCrawler 项目的兴趣！本文档描述如何参与本项目贡献。

## 行为准则

参与本项目的每一位贡献者都需要遵守 [Code of Conduct](CODE_OF_CONDUCT.md)。请在所有交流中保持尊重与专业。

## 法律声明

AegisCrawler 基于 **GPL-3.0-or-later** 协议开源。提交到本仓库的所有贡献：

- 默认以 GPL-3.0-or-later 协议授权，与项目主协议一致；
- 不要求签署 CLA（Contributor License Agreement），但提交者需保证拥有代码的完整版权或已获得相应授权；
- 如贡献者受雇于公司，请确认公司知识产权政策允许该贡献（必要时请公司签署免责声明）。

## 开发环境准备

- Node.js 20+
- Go 1.24.2+
- Docker 24+（用于本地集成测试）
- 推荐使用 macOS / Linux；Windows 用户建议使用 WSL2

```bash
git clone https://github.com/singhand-labs/AegisCrawler.git
cd AegisCrawler
npm ci
cd server && go mod download && cd ..
```

## 开发流程

### 1. Fork 并创建分支

```bash
git checkout -b feat/your-feature
# 或
git checkout -b fix/issue-123
```

分支命名约定：

- `feat/*`：新功能
- `fix/*`：bug 修复
- `docs/*`：文档
- `refactor/*`：重构（不改变行为）
- `test/*`：测试补充
- `chore/*`：构建/依赖/CI 等

### 2. 编码与测试

**测试是必须的**：

```bash
# TypeScript
npm run lint
npm test                # vitest + admin-ui
npm run test:coverage   # 覆盖率阈值 90%

# Go
cd server
go test ./...
go vet ./...
go build ./...
```

新增功能必须附带测试，并保持覆盖率不低于 90%。修复 bug 应当附带能复现该 bug 的回归测试。

### 3. Commit 规范

遵循 [Conventional Commits](https://www.conventionalcommits.org/zh-hans/)：

```
<type>(<scope>): <subject>

<body>

<footer>
```

- **type**：`feat` / `fix` / `docs` / `style` / `refactor` / `perf` / `test` / `build` / `ci` / `chore`
- **scope**（可选）：影响的模块，如 `server`、`extension`、`admin-ui`、`rule-engine`、`scheduler`、`docs`
- **subject**：祈使句，首字母小写，≤72 字符，不加句号

示例：

```
feat(scheduler): add per-worker concurrency limit
fix(llm): handle nil response from anthropic provider
docs(deployment): add caddy reverse proxy example
test(rule-engine): cover nested switch action
```

### 4. 提交 Pull Request

- PR 标题沿用 commit 规范；
- 一个 PR 只解决一个独立问题；
- 在 PR 描述中关联相关 Issue（`Closes #123`）；
- 等待 CI（Node lint/test、Go test、e2e）全部通过；
- 至少一名 maintainer review 通过后方可合并。

## 代码风格

- **Go**：`gofmt` + `goimports`，错误处理显式返回，避免 panic；
- **TypeScript**：`tsc --noEmit` 必须通过，优先使用 `type` 而非 `interface` 定义纯类型；
- **React**：函数式组件 + Hooks，测试使用 `@testing-library/react`；
- 缩进 2 空格（TS/React/YAML/JSON）、Tab（Go，由 gofmt 保证）；
- 文件末尾保留一个空行。

## 关于测试中的占位密钥

仓库的测试文件中会出现诸如 `'test-admin-key'`、`'secret-key'`、`'manual-worker-key-for-testing'`、`'swagger-secret'` 等字符串。**这些是测试 fixture，不是真实凭证**：

- 它们只存在于 `*_test.go` / `*.test.ts` / `scripts/start-manual-test-env.ts` 等测试或本地辅助脚本中；
- 不会进入生产构建的二进制或运行时配置；
- `.env.example` 中的所有 `change-me-*` 也是占位值；
- 贡献者**禁止**在测试代码中引入任何真实的 API key、数据库密码或云凭证；
- 真实 LLM 集成测试（`RUN_LLM_INTEGRATION=1`）需要本地注入 `LLM_API_KEY` 环境变量，**禁止提交包含真实 key 的代码或文档**。

## 文档变更

修改用户可见行为时请同步更新：

- `README.md`：核心功能、快速开始、配置项；
- `docs/`：部署、运维、安全、DSL 设计等专题文档；
- Swagger 注释（Go 侧 `@Summary` / `@Param` 等）：修改 API 后运行 `swag init` 重新生成 `server/docs/`。

## 报告安全问题

请不要通过 GitHub Issue 公开披露安全漏洞。请参阅 [SECURITY.md](SECURITY.md) 中的私下披露流程。

## 问题与讨论

- Bug / 功能请求：[GitHub Issues](https://github.com/singhand-labs/AegisCrawler/issues)
- 通用讨论：[GitHub Discussions](https://github.com/singhand-labs/AegisCrawler/discussions)（如未开启请先在 Issue 中讨论）

再次感谢你的贡献！
