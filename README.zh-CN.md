# AegisCrawler PageResearch Agent Service

[![CI](https://github.com/singhand-labs/AegisCrawler/actions/workflows/ci.yml/badge.svg)](https://github.com/singhand-labs/AegisCrawler/actions/workflows/ci.yml)
[![License: GPL-3.0-or-later](https://img.shields.io/badge/license-GPL--3.0--or--later-blue.svg)](LICENSE)
[![Docker](https://img.shields.io/badge/docker-ghcr.io-2496ED?logo=docker&logoColor=white)](https://github.com/singhand-labs/AegisCrawler/pkgs/container/aegiscrawler)

[English](README.md) | **简体中文**

一个面向生产环境的“录制即规则、规则即任务”的浏览器数据采集平台。

- **PageResearch Agent 浏览器扩展**：在真实页面上录制点击、输入、滚动、拖拽等操作，自动生成可执行的 DSL 规则。
- **Go 服务端**：规则 / 任务 / 结果 / 日志 / 审计的唯一真相源，提供 OpenAPI/Swagger 接口。
- **ScriptCat Worker**：在浏览器端稳定执行 DSL 规则，采集数据并回传服务端。

> 设计哲学：PageResearch Agent 负责**快速生成规则原型**；ScriptCat + 服务端负责**长期稳定、可调度、可观测的采集执行**。

![AegisCrawler 演示：录制浏览器操作、审阅生成的 DSL、作为任务执行](.github/assets/demo.gif)

---

## 目录

- [核心特性](#核心特性)
- [系统架构](#系统架构)
- [项目结构](#项目结构)
- [快速开始](#快速开始)
- [配置说明](#配置说明)
- [API 文档](#api-文档)
- [Admin UI 管理后台](#admin-ui-管理后台)
- [部署指南](#部署指南)
- [运维与监控](#运维与监控)
- [本地开发](#本地开发)
- [安全说明](#安全说明)
- [法律免责声明](#法律免责声明)
- [许可证](#许可证)

---

## 核心特性

- **拟人化操作 DSL**：支持 click、type、scroll、drag、slide、upload、hover、wait、evaluate、loop、if、extract 等全场景动作。
- **规则录制与生成**：浏览器扩展录制用户交互，自动产出 YAML/JSON 规则。
- **任务调度与恢复**：基于 lease 的任务认领、心跳续租、sweeper 自动回收与重试、死信队列。
- **Admin 管理面**：规则的完整 CRUD、任务 cancel/retry、审计日志查询。
- **Admin UI 管理后台**：基于 React 的中文可视化界面，支持规则、单次/定时任务、调度计划、审计日志管理；规则详情页提供 DAG 流程图 + 结构化列表视图，把复杂 DSL 一键转换为普通用户可理解的操作过程。
- **LLM 规则增强（可选）**：服务端对接 OpenAI / Anthropic 等兼容 API，对 PageResearch Agent 录制的 baseline 规则进行 patch 增强；增强结果经安全扫描后生成 pending 规则，管理后台提供 diff 审核与一键应用/拒绝。
- **规则可视化**：将规则的 `steps`、`if/switch/loop/parallel`、生命周期钩子等渲染为可交互的 DAG（基于 @antv/g6），并保留原始 JSON 调试视图与列表降级。
- **任务取消**：Admin UI/API 标记取消后，Worker 通过心跳响应立即中止正在执行的规则，并上报 `cancelled` 状态。
- **生产级韧性**：请求体限制、全局超时、熔断、分级限流（全局 / worker / 站点）、Worker SDK 退避重试与缓冲。
- **可观测性**：结构化日志（zap）、Prometheus 指标、健康检查（含数据库 readiness）、DB size 指标。
- **SQLite WAL + 版本化迁移**：纯 Go 依赖（`modernc.org/sqlite`），无需 CGO。
- **CI/CD 发布**：版本 tag 自动构建多平台 Docker 镜像推送 GHCR，并发布浏览器扩展 zip 与 userscript。

---

## 系统架构

```
┌─────────────────────────────────────────────────────────────────────┐
│                        浏览器 (Browser)                              │
│                                                                     │
│  ┌─────────────────────────┐    ┌────────────────────────────────┐  │
│  │ PageResearch Agent 扩展           │    │ ScriptCat 扩展 / Worker        │  │
│  │ · content script 录制    │    │ · 短轮询 /tasks/claim          │  │
│  │ · popup 生成/上传规则    │    │ · GM_openInTab 打开目标页      │  │
│  │ · background 状态管理    │    │ · 注入 userscript 执行 DSL     │  │
│  └───────────┬─────────────┘    └──────────────┬─────────────────┘  │
│              │                                  │                    │
│              │ POST /admin/rules                │ POST /results      │
│              │                                  │ POST /logs         │
└──────────────┼──────────────────────────────────┼────────────────────┘
               │                                  │
               ▼                                  ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    AegisCrawler Server (Go)                         │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌───────────┐  │
│  │ Admin API   │  │ Worker API  │  │ Scheduler   │  │ Store     │  │
│  │ /admin/*    │  │ /tasks/claim│  │ lease/retry │  │ SQLite    │  │
│  │ /health     │  │ /results    │  │ sweeper     │  │ WAL       │  │
│  │ /metrics    │  │ /heartbeat  │  │ retention   │  │ migrations│  │
│  └─────────────┘  └─────────────┘  └─────────────┘  └───────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

1. 用户在目标站点操作，PageResearch Agent 录制并生成 DSL 规则。
2. Admin 将规则上传到 Server，审核/启用后创建任务。
3. ScriptCat Worker 轮询认领任务，打开目标页面并执行规则。
4. 执行结果、日志、状态、快照等回写到 Server。
5. Admin 通过 `/admin/*` 查询任务、审计日志、重试/取消任务。

---

## 项目结构

```
.
├── server/               # Go 服务端
│   ├── cmd/server/       # 入口 main.go
│   ├── internal/api/     # HTTP 接口、中间件
│   ├── internal/store/   # SQLite 数据访问
│   ├── internal/scheduler/ # 调度、lease、sweeper
│   ├── internal/config/  # 环境变量配置
│   └── docs/             # Swagger 文档
├── src/
│   ├── rule-generator/   # PageResearch Agent 录制 → DSL 规则
│   ├── scriptcat-engine/ # DSL 执行器
│   ├── rule-engine/      # 规则校验与 schema
│   └── worker/           # Worker SDK（HttpTransport、Worker、BrowserEnvironment）
├── extension/            # 浏览器扩展源码
│   ├── src/
│   └── manifest.json
├── scripts/              # 构建 userscript / extension / 测试脚本
├── examples/             # DSL 规则示例
├── dist/                 # 构建产物
└── docker-compose.yml
```

---

## 快速开始

### 1. 克隆与安装依赖

```bash
git clone https://github.com/singhand-labs/AegisCrawler.git
cd AegisCrawler

# Node 依赖
npm ci

# Go 依赖
cd server && go mod download && cd ..
```

### 1.5 命令行用法（可选）

服务端二进制内置 CLI（`cd server && go build -o aegiscrawler ./cmd/server`）：

```bash
./aegiscrawler                      # 等同于 serve（兼容历史无参数调用）
./aegiscrawler serve -c config.local.yml   # 指定配置文件启动
./aegiscrawler validate-config -c config.yml  # 校验 env+config.yml，不启服务（CI 友好）
./aegiscrawler migrate -c config.local.yml    # 仅执行数据库迁移
./aegiscrawler version             # 版本（构建时 -ldflags "-X main.version=x.y.z" 注入）
```

`-c/--config` 优先于 `CONFIG_PATH` 环境变量；配置值的优先级仍是 环境变量 > config.yml > 默认值。

### 2. 启动服务端

推荐使用 Docker Compose（需要 Docker 24+）：

```bash
# 项目根目录创建 .env
cat > .env <<EOF
WORKER_API_KEY=change-me-worker-key-long-and-random
ADMIN_API_KEY=change-me-admin-key-long-and-random
VARIABLE_ENCRYPTION_KEY=change-me-encryption-key-at-least-32-characters-long
METRICS_API_KEY=change-me-metrics-key
SWAGGER_API_KEY=change-me-swagger-key
LLM_ENABLED=false
# Compose 要求显式选择；生产启用 LLM 时必须改为 enforced 并配置完整策略。
LLM_POLICY_MODE=legacy
EOF

export WORKER_API_KEY=$(grep WORKER_API_KEY .env | cut -d '=' -f2-)
export ADMIN_API_KEY=$(grep ADMIN_API_KEY .env | cut -d '=' -f2-)
export VARIABLE_ENCRYPTION_KEY=$(grep VARIABLE_ENCRYPTION_KEY .env | cut -d '=' -f2-)

docker compose up -d
```

验证：

```bash
curl -s http://localhost:8080/health
curl -s http://localhost:8080/health?ready=1
```

### 3. 加载 PageResearch Agent 扩展

```bash
npm run build:extension
```

然后浏览器“加载已解压的扩展程序”，选择 `dist/extension/`。

### 4. 录制并上传规则

1. 打开目标站点，点击扩展 popup 的“开始录制”。
2. 录制过程中可点击页面右下角“标注采集意图”或按 `Alt+M`，圈选元素并填写“大白话”备注，例如“商品标题字段”或“排除广告区域”。这些标注会帮助需求与 DSL 生成理解采集意图，不会保存截图。
3. 完成要采集的操作序列。
4. 点击“停止录制”，在意图确认向导中检查/编辑录制标注、确认采集目的、预览生成的 DSL、观看完整回放验证。
5. 配置 Server Base URL 和 Admin API Key 后保存上传规则。

### 5. 创建任务并运行 Worker

推荐通过 Admin UI 创建任务：访问 `http://localhost:8080/admin/`，进入「创建任务」，选择规则模板并提交。

也可通过 Admin API 创建任务（示例）：

```bash
curl -X POST http://localhost:8080/admin/tasks \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "ruleId": "<rule-uuid>",  # 替换为 Admin UI 中规则列表里实际的规则 ID
    "variables": { "keyword": "手机" }
  }'
```

然后在已安装 ScriptCat 的浏览器中加载 `dist/aegiscrawler-0.2.0.user.js`，Worker 会自动轮询并执行任务。

### LLM 增强使用流程（可选）

1. 生产环境按下方「LLM 增强配置」的 enforced 策略要求配置
   `LLM_ENABLED=true`、`LLM_POLICY_MODE=enforced`、完整 provider route、价格、
   硬预算与自动尝试上限。`LLM_POLICY_MODE=legacy` 和通用 `LLM_API_KEY` 路径
   仅用于本地兼容或迁移，不得承载生产 LLM 流量。
2. 录制完成后，在扩展 popup 点击「AI 增强生成」。
3. 扩展将录制数据与 baseline 规则发送到 `POST /admin/rules/enhance`。
4. 服务端调用 LLM 生成增强建议，写入 `rule_enhancements` 表，并生成状态为 `pending` 的规则。
5. 在 Admin UI 规则详情页查看 baseline 与增强后规则的 diff，确认后点击「应用」生效，或点击「拒绝」丢弃。

> 即使 LLM 未启用或调用失败，扩展仍可上传确定性 baseline 规则，服务不会崩溃。

---

## 配置说明

生产环境必须设置以下三项，且建议保持 `REQUIRE_SECURITY_KEYS=true`（默认值）：

| 变量 | 必填 | 说明 |
|---|---|---|
| `WORKER_API_KEY` | 是 | Worker 接口的 Bearer Token |
| `ADMIN_API_KEY` | 是 | Admin 接口的 Bearer Token |
| `VARIABLE_ENCRYPTION_KEY` | 是 | 敏感任务变量加密密钥，建议 ≥32 字符 |
| `METRICS_API_KEY` | 否 | `/metrics` 保护 Token |
| `SWAGGER_API_KEY` | 否 | Swagger UI/Spec 保护 Token |

关键运行参数：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | HTTP 监听地址 |
| `DATABASE_PATH` | `opencrawler.db` | SQLite 路径 |
| `LEASE_DURATION` | `60s` | Worker 任务租约时长 |
| `SWEEPER_INTERVAL` | `30s` | 过期 lease 回收间隔 |
| `MAX_RETRIES` | `3` | 任务最大重试次数 |
| `MAX_WORKER_TASKS` | `5` | 每个 Worker 最大并发任务数 |
| `RATE_LIMIT_PER_SECOND` / `BURST` | `100` / `150` | 全局限流 |
| `WORKER_RATE_LIMIT_PER_SECOND` / `BURST` | `10` / `15` | 单 Worker 限流 |
| `SITE_RATE_LIMIT_PER_SECOND` / `BURST` | `5` / `8` | 单目标站点限流 |
| `CIRCUIT_BREAKER_*` | — | 熔断阈值与窗口 |
| `MAX_REQUEST_BODY_BYTES` | `8MB` | 最大请求体 |
| `REQUEST_TIMEOUT` | `60s` | 全局请求超时 |
| `SQLITE_JOURNAL_MODE` | `WAL` | SQLite 日志模式 |

### LLM 增强配置

生产启用 LLM 必须使用 `LLM_POLICY_MODE=enforced`；完整的
`LLM_PRIMARY_*`、可选 `LLM_FALLBACK_*`、价格、硬预算与自动尝试上限见
完整策略要求：enforced 模式必须配置多 provider route、价格、硬预算（`WORKFLOW_BUDGET_USD_CENTS`）与自动尝试上限。下表中的通用
provider、key、model、retry 和 timeout 变量属于 `legacy` 兼容路径，仅用于
本地开发或迁移。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LLM_ENABLED` | `false` | 是否启用 AI 增强 |
| `LLM_POLICY_MODE` | `legacy`（Compose 要求显式设置） | 生产启用 LLM 时必须为 `enforced` |
| `LLM_PROVIDER` | `openai` | 仅 legacy：主 provider，目前支持 `openai` / `anthropic` |
| `LLM_FALLBACK_PROVIDER` | `""` | 仅 legacy：失败后的备用 provider |
| `LLM_API_KEY` | `""` | 仅 legacy：LLM API 密钥 |
| `LLM_BASE_URL` | `https://api.openai.com/v1` | 仅 legacy：OpenAI 兼容 endpoint |
| `LLM_MODEL` | `gpt-4o` | 仅 legacy：模型名 |
| `LLM_TEMPERATURE` | `0.2` | 仅 legacy：采样温度 |
| `LLM_MAX_RETRIES` | `2` | 仅 legacy：主 provider 重试次数 |
| `LLM_REQUEST_TIMEOUT` | `60s` | 仅 legacy：单次请求超时 |
| `LLM_ENABLE_REFLECTION` | `true` | 是否开启反思校验 |
| `LLM_CACHE_TTL` | `24h` | 结果缓存时间 |

完整配置见 `server/internal/config/config.go` 与 `docker-compose.yml`。

### config.yml 配置文件

除环境变量外，所有配置也可以写入 `config.yml`（扁平键名与环境变量一一对应）：

```
LISTEN_ADDR: ":8080"
DATABASE_PATH: opencrawler.db
MAX_RETRIES: 5
FEATURE_WORKFLOW_V2: true
```

- **优先级**：环境变量 > `config.yml` > 内置默认值。同名配置以环境变量为准。
- 文件路径默认为工作目录下的 `config.yml`，可用环境变量 `CONFIG_PATH` 指定其他位置（该变量只认环境变量）。
- 布尔写 `true/false`；数字直接写；时长写带单位的字符串（如 `60s`、`5m`）；JSON 型变量（如 `LLM_PROVIDER_CONFIGS`）可直接写 YAML 映射或数组，加载时自动转为 JSON。
- 文件不存在时按纯环境变量模式运行；文件存在但解析失败会**拒绝启动**（fail-closed）。
- 完整示例见 [`server/config.example.yml`](server/config.example.yml)。

---

## API 文档

服务端遵循 OpenAPI 规范，Swagger 文档自动生成。

启动服务后访问：

```
http://localhost:8080/swagger/index.html
```

主要接口分类：

- **Worker API**：`/tasks/claim`、`/rules/{id}`、`/results`、`/logs`、`/status`、`/heartbeat`、`/checkpoints`
- **Admin API**：`/admin/rules`、`/admin/tasks`、`/admin/tasks/{id}/cancel`、`/admin/tasks/{id}/retry`、`/admin/schedules`、`/admin/schedules/preview`、`/admin/audit_logs`
- **录制工作流 API**：`/api/v1/recordings`、`/api/v1/requirements`、`/api/v1/dsl-workflows`、`/api/v1/dsl-replays`
- **系统**：`/health`、`/health?ready=1`、`/metrics`





---

## Admin UI 管理后台

服务启动后，通过浏览器访问：

```
http://localhost:8080/admin/
```

首次打开需要输入 `ADMIN_API_KEY`。后台提供：

- **规则管理**：创建、编辑、启用/禁用、审核规则。
- **单次任务**：选择规则模板，填写变量覆盖，可指定未来执行时间。
- **定时调度**：选择规则模板，使用 Cron 预设或自定义表达式，实时预览未来 5 次执行时间，设置漏跑策略（跳过/补跑一次）。
- **任务运维**：查看任务状态、结果、日志；支持取消运行中任务、重试失败任务。
- **审计日志**：记录管理员关键操作。

> **注意**：Admin UI 静态资源通过 `go:embed` 打包进服务端二进制。构建前请执行 `npm run build:admin`，否则 `/admin/` 会返回空白页面。

---

## 部署指南

生产部署要点（详细运维手册暂随源码本地维护）：

1. **不要直接暴露 HTTP**：务必通过反向代理（Caddy/nginx/Traefik）做 TLS 终结。
2. **使用 GHCR 镜像**：
   ```bash
   docker pull ghcr.io/singhand-labs/aegiscrawler:latest
   ```
3. **数据库备份**：SQLite 文件 `/data/opencrawler.db` 定期备份。
4. **密钥管理**：`WORKER_API_KEY`、`ADMIN_API_KEY`、`VARIABLE_ENCRYPTION_KEY` 必须强随机，且与数据库备份分开存放。

---

## 运维与监控

（详细运维手册暂随源码本地维护。）

- **健康检查**：`GET /health`（liveness）、`GET /health?ready=1`（readiness，检查数据库）。
- **Prometheus 指标**：`GET /metrics`，包含请求数、任务认领/完成数、结果数、DB size 等。
- **日志**：结构化 JSON 日志，每条请求携带 `traceId`。
- **死信任务**：超过 `MAX_RETRIES` 的任务进入 `dead_letter`，需人工排查后重建任务。
- **数据保留**：支持结果、日志、快照、心跳、状态更新、检查点、已完成任务的独立保留策略。

---

## 本地开发

### 运行测试

```bash
# TypeScript 测试
npm run lint
npm test

# Go 测试
cd server
go test ./...
go vet ./...
go build ./...
```

### LLM 增强功能测试

规则增强（`/admin/rules/enhance`）默认不调用真实 LLM。项目包含一个可选的集成测试，使用 fake LLM provider 验证 enhance → get → accept/reject 完整流程：

```bash
cd server
RUN_LLM_INTEGRATION=1 go test -tags llm_integration ./internal/llm/... -v -run TestIntegrationEnhanceGetAcceptRejectFlow
```

若要用真实 LLM key 运行集成测试，可设置以下环境变量（以 OpenAI 为例）：

```bash
export LLM_ENABLED=true
export LLM_PROVIDER=openai
export LLM_API_KEY=sk-...
export LLM_BASE_URL=https://api.openai.com/v1   # 可选，兼容兼容 OpenAI 格式的代理
export LLM_MODEL=gpt-4o
export LLM_TEMPERATURE=0.2
export RUN_LLM_INTEGRATION=1
go test -tags llm_integration ./internal/llm/... -v -run TestIntegrationEnhanceRealLLM
```

> 注意：真实 LLM 调用会产生费用并依赖网络，请在 CI 中保持默认跳过；本地验证时确保 `LLM_API_KEY` 不泄露。

### 重新生成 Swagger

```bash
cd server
go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/server/main.go
```

### 构建产物

```bash
npm run build              # 编译 TypeScript
npm run build:admin        # 构建 Admin UI 并嵌入 server/web/admin
npm run build:userscript   # 生成 userscript
npm run build:extension    # 生成扩展目录
npm run package:extension  # 打包为 zip
```

---

## 安全说明

- 生产环境设置 `REQUIRE_SECURITY_KEYS=true`，服务端在缺少密钥时直接退出。
- Admin 接口通过 `Authorization: Bearer <ADMIN_API_KEY>` 保护。
- Worker 接口通过 `Authorization: Bearer <WORKER_API_KEY>` 保护。
- 敏感变量通过 `VARIABLE_ENCRYPTION_KEY` 加密后存储。
- `evaluate` action 默认受限；开启 `allowEvaluateDOM` 会暴露 DOM，仅在受信任规则中使用。
- 用户脚本/扩展应通过组织内部的扩展商店或企业策略分发，避免公开传播密钥。

---

## 法律免责声明

本项目（AegisCrawler）是一个通用的浏览器自动化与数据采集工具，仅提供录制、执行 DSL 规则和管理任务的技术能力。使用者应当：

- **遵守当地法律法规**，包括但不限于著作权法、反不正当竞争法、数据安全法、个人信息保护法（PIPL）、GDPR 等相关规定；
- **遵守目标网站的服务条款（ToS）与 robots.txt 约定**，在授权或许可范围内进行访问和采集；
- **不得将本工具用于**以下用途：未授权访问受保护的系统、绕过身份认证或反爬措施、抓取个人隐私数据、侵犯他人知识产权、破坏目标系统的正常运行、进行拒绝服务攻击等违法或侵权行为；
- 自行评估并承担使用本工具带来的全部法律责任和风险，项目作者和维护者不承担任何因使用或滥用本工具而产生的直接或间接责任。

如果在目标站点使用本工具前未取得明确授权，请优先联系站点所有者获取许可，或寻求官方提供的 API/数据获取渠道。

## 许可证

本项目基于 [GNU General Public License v3.0 or later](LICENSE)（GPL-3.0-or-later）开源。

- 任何基于本项目的二次开发、分发或组合发布，必须同样以 GPL-3.0-or-later 协议开放源代码；
- 闭源商业化使用（例如将本项目作为核心模块嵌入到不开源的 SaaS 或私有部署产品中对外提供）需要获得版权所有者的另行授权，请联系 zy@singhand.com；
- 依赖库保持各自的原协议，本项目不改变它们的协议。
