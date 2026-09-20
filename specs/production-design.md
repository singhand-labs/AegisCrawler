# AegisCrawler Rule Engine - 生产级设计

## 1. 设计目标

本规则引擎面向长期、规模化、可观测的数据采集场景，核心目标按优先级排序：

1. **数据完整性优先**：任何情况下优先把已采集到的有效数据发回服务端，再处理失败。
2. **快速失败与可观测**：关键步骤失败后秒级上报，附带错误类型、截图、上下文变量。
3. **可恢复**：支持 checkpoint、lease、heartbeat，worker 崩溃后可由其他 worker 接管。
4. **可控制**：配额、熔断、并发、速率限制，避免对目标站点造成过大压力。
5. **可维护**：规则版本化、灰度发布、选择器健康检查、失败自动告警。

## 2. 执行生命周期

```
Created → Leased → Running → [Checkpoint] → Extracting → Flushing → Done
   │         │         │          │            │           │
   │         │         ▼          │            │           ▼
   │         │    onError hook    │            │       Failed (with partial data)
   │         │         │          │            │
   │         ▼         ▼          ▼            ▼
   │    LeaseExpired ──► Requeued ──► Retried ──► DeadLetter
   │
   ▼
Cancelled
```

### 状态说明

| 状态 | 含义 |
|------|------|
| `pending` | 任务已创建，等待 worker 拉取 |
| `leased` | 已被 worker 认领，lease 有效期内必须完成或续期 |
| `running` | 正在执行规则步骤 |
| `paused` | 等待人工处理（验证码、2FA） |
| `done` | 成功完成，输出数据已校验 |
| `failed` | 失败，但已尽力 flush 部分数据 |
| `cancelled` | 被人工或策略取消 |
| `dead_letter` | 重试耗尽，进入死信队列待人工介入 |

## 3. 快速失败与部分上报

### 3.1 失败时的黄金原则

> **“先把采到的数据送回去，再谈失败。”**

每条规则必须声明 `sendPolicy.sendOnFailure: true`（默认应为 true）。执行器在以下时机触发 `flushResults`：

- 任意 `critical: true` 步骤失败；
- 规则级 `onError` hook 被触发；
- `abort` action 执行时设置 `flushBeforeAbort: true`；
- worker 被关闭前（SIGTERM/SIGINT 处理）。

### 3.2 失败信息必须包含

```json
{
  "taskId": "...",
  "ruleId": "...",
  "status": "failed",
  "error": {
    "type": "ElementNotFound",
    "message": "未能找到 .product-card",
    "stepId": "extract-page",
    "retryCount": 3,
    "timestamp": "2026-07-03T08:30:00Z"
  },
  "context": {
    "lastUrl": "https://...",
    "variables": { "pageIndex": 2 },
    "extractedSoFar": { "items": 47 }
  },
  "partialData": { ... },
  "snapshot": { "screenshot": "base64...", "html": "..." }
}
```

## 4. 错误分类与处理策略

| 错误类型 | 触发场景 | 默认策略 |
|----------|---------|---------|
| `ElementNotFound` | 选择器找不到元素 | 重试 3 次 → 失败 |
| `ElementNotVisible` | 元素存在但不可见 | 重试 → 截图 → 失败 |
| `TimeoutError` | 步骤超时 | 重试 → 失败 |
| `NavigationError` | 页面跳转失败 | 重试 → 失败 |
| `NetworkError` | 网络断开或 DNS 失败 | 指数退避重试 |
| `HttpError` | HTTP 4xx/5xx | 4xx 不重试，5xx 重试 |
| `ValidationError` | 提取结果不符合 output schema | 发送部分数据 → 失败 |
| `AuthenticationError` | 登录态丢失 | 直接失败 |
| `CaptchaError` | 遇到验证码 | 请求人工 / 打码服务 |
| `RateLimited` | 被限流 | 等待冷却 → 重试 |
| `Blocked` | IP/账号被封 | 切换代理 → 重试 |
| `SessionExpired` | session 过期 | 触发 refreshSession → 重试 |
| `PageCrashed` | 页面崩溃 | 重载 → 从 checkpoint 恢复 |
| `ScriptError` | 自定义脚本异常 | 记录日志 → 失败 |
| `QuotaExceeded` | 超出配额 | 等待或失败 |
| `HumanTimeout` | 人工处理超时 | 失败 |
| `UnknownError` | 未分类错误 | 重试 1 次 → 失败 |

## 5. 恢复机制

### 5.1 Checkpoint

关键步骤前可声明 `checkpoint: true`。执行器将当前状态持久化到服务端：

```yaml
- action: extract
  name: pageItems
  checkpoint: true
  target: { selector: ".product-card" }
  multiple: true
```

持久化内容：
- 当前 URL；
- 已提取数据；
- 循环索引等运行时变量；
- 最后成功执行的 stepId。

### 5.2 Lease 与 Heartbeat

- Worker 拉取任务时获得 lease（默认 60s）；
- 执行期间每 30s 发送 `heartbeat` 续期；
- 服务端 `sweeper` 检查 lease 过期任务，重新入队；
- 新 worker 认领后可从最后一个 checkpoint 恢复。

### 5.3 Recover Action

```yaml
- action: recover
  checkpointName: after-login
  steps:
    - action: navigate
      url: "{{checkpoint.url}}"
```

## 6. 可观测性

### 6.1 日志

每条规则执行产生结构化日志：

- `step_start` / `step_end` / `step_error`；
- `extract_count`；
- `flush_results`；
- `checkpoint_saved`；
- `human_requested` / `human_resolved`。

### 6.2 指标

通过 `logMetric` action 和引擎自动上报：

- `task_duration_seconds`；
- `extracted_items_total`；
- `task_failures_total`（按 errorType 分标签）；
- `selector_not_found_total`；
- `http_request_duration_seconds`；
- `queue_wait_seconds`。

### 6.3 追踪

每个 task 生成唯一 traceId，所有日志、指标、数据包都带 traceId，便于全链路排查。

## 7. 配额与熔断

### 7.1 配额（CheckQuota）

```yaml
- action: checkQuota
  type: requestsPerMinute
  limit: 60
  onExceeded: wait
  cooldownMs: 60000
```

服务端维护 per-site / per-worker / per-account 配额计数器。

### 7.2 熔断（CircuitBreaker）

```yaml
- action: circuitBreaker
  name: example-site
  failureThreshold: 10
  windowMs: 60000
  cooldownMs: 300000
```

当某站点在 60s 内失败 10 次，熔断器打开，暂停该站点任务 5 分钟。

## 8. 安全与沙箱

### 8.1 ScriptCat 执行环境

- page script 在目标页隔离上下文中运行，不直接暴露 server API key；
- 敏感变量（密码、token）通过服务端加密下发，page script 不解密；
- 自定义 `evaluate` 脚本必须走沙箱或白名单，禁止访问 `localStorage` 之外的敏感 API。

### 8.2 数据安全

- PII 字段在规则中显式标记，采集后脱敏；
- 传输使用 HTTPS；
- 快照、日志保留 TTL，过期自动清理。

## 9. 数据完整性与幂等

### 9.1 幂等键

- 每个 task 生成 `taskId`（uuid）；
- `sendResult` 的每条记录带 `recordId`（由业务 key 生成，如 sku+site+date）；
- 服务端按 `recordId` 去重。

### 9.2 输出校验

规则声明 `output` JSON Schema。任务结束前必须执行 `validateData`：

```yaml
- action: validateData
  from: finalItems
  schema:
    type: array
    minItems: 1
    items:
      required: [sku, name, price]
  onInvalid: fail
```

### 9.3 数据血缘

每条记录附加：

```json
{
  "_meta": {
    "taskId": "...",
    "ruleId": "...",
    "ruleVersion": "1.0.0",
    "workerId": "...",
    "collectedAt": "...",
    "url": "..."
  }
}
```

## 10. 扩展性

### 10.1 Worker 并发模型

- 每个 worker 单线程事件循环，同时处理 N 个 tab；
- `per-worker` 并发由服务端配置；
- 任务按 domain 分组队列，避免单站点并发过高。

### 10.2 规则热更新

- Worker 启动时拉取全量规则；
- 服务端推送规则变更事件（SSE / WebSocket / 轮询）；
- 灰度发布：先 5% worker 加载新版本，观察 10 分钟后再全量。

## 11. 灾难场景应对

| 场景 | 应对 |
|------|------|
| Worker 进程崩溃 | lease 过期，任务重新入队，新 worker 从 checkpoint 恢复 |
| 浏览器 tab 崩溃 | `PageCrashed` 错误，重载页面，从 checkpoint 恢复 |
| 目标站点全面改版 | 选择器健康检查告警，PageResearch Agent 重新录制，规则版本升级 |
| 代理全部失效 | 熔断 + 告警，任务暂停，人工更换代理池 |
| 大规模验证码 | 触发 human-in-the-loop，或临时切换打码服务 |
| 数据量突增 | 自动分片、限流、扩容 worker |

## 12. 推荐的最小生产配置

```yaml
rule:
  timeout: 120000
  maxRetries: 2
  screenshotOnError: true
  sendPolicy:
    batchSize: 30
    flushInterval: 15000
    sendOnFailure: true
  hooks:
    onError:
      - action: saveSnapshot
      - action: flushResults
    cleanup:
      - action: navigate
        url: "about:blank"
```
