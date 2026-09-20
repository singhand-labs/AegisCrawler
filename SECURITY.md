# 安全策略

本文件描述 AegisCrawler 项目的漏洞披露政策与支持版本范围。

> 如果你只是想了解如何在生产环境安全部署本服务，请参阅 [`docs/security.md`](docs/security.md) 与 [`docs/deployment.md`](docs/deployment.md)。
> 本 SECURITY.md 专门针对**项目自身代码中的安全漏洞**的报告与修复流程。

## 支持的版本

AegisCrawler 仍处于 0.x 阶段，仅对最新一个 minor 分支提供安全更新。

| 版本 | 是否支持安全更新 |
|------|------------------|
| 最新 0.x release（`v0.*` 的最新 tag） | ✅ 支持 |
| 旧 0.x 版本 | ❌ 不支持（请升级至最新版本） |
| main 分支 | ⚠️ 尽力而为（请以 release tag 为准） |

## 如何报告安全漏洞

**请不要通过公开的 GitHub Issue 披露安全漏洞。**

请通过以下任一渠道私下报告：

1. **首选**：使用 GitHub 私密漏洞报告（仓库 Security 标签页 → "Report a vulnerability"）；
2. **备用**：发送邮件至 **zy@singhand.com**，主题以 `[SECURITY] AegisCrawler` 开头。

请在报告中包含以下信息，以帮助我们尽快定位与修复：

- 受影响的版本（commit hash 或 release tag）；
- 漏洞类型（如 SSRF、SQL 注入、XSS、鉴权绕过、敏感信息泄露等）；
- 复现步骤（最小可复现示例最佳）；
- 已确认的影响范围与潜在攻击场景；
- 建议的修复方案（可选）。

## 响应时间

- **确认收悉**：维护者将在 **3 个工作日**内回复你的报告；
- **初步评估**：在 **7 个工作日**内给出漏洞严重程度评估与修复计划；
- **发布修复**：根据严重程度，严重漏洞（CVSS ≥ 7.0）通常在 **30 天**内发布修复版本；中低危漏洞会合并至下一个常规 release。

## 披露政策

- 在修复版本发布前，我们会与你协商是否进行协调披露（Coordinated Disclosure）；
- 我们不会在你不同意的情况下提前公开漏洞细节；
- 修复发布后，我们会在 GitHub Release Notes 与安全公告中致谢报告者（除非你希望匿名）。

## 范围与不在范围

### 在范围内

- AegisCrawler 仓库本身（Go 服务端、TypeScript 核心库、浏览器扩展、Admin UI）的代码漏洞；
- 官方 Docker 镜像（`ghcr.io/singhand-labs/aegiscrawler`）中的配置问题；
- CI/CD 工作流（`.github/workflows/`）中的权限或密钥泄露问题。

### 不在范围内

- 使用本工具进行爬取时，目标站点的安全漏洞；
- 因使用者配置不当（例如使用弱密钥、未启用 `REQUIRE_SECURITY_KEYS`、把 `.env` 提交到公共仓库等）导致的自损；
- 对 `examples/` 中示例规则所采集的第三方站点的任何测试，除非你拥有该站点或已获得书面授权；
- 拒绝服务（DoS）漏洞的演示，除非附带明确的修复建议且影响真实部署形态；
- 第三方依赖的已知 CVE（请直接向其上游报告，并通过 Dependabot 在本项目内升级修复版本）。

## 安全最佳实践速览

- 生产环境必须设置 `WORKER_API_KEY`、`ADMIN_API_KEY`、`VARIABLE_ENCRYPTION_KEY`，且保持 `REQUIRE_SECURITY_KEYS=true`；
- 所有密钥使用强随机（≥32 字符），通过密钥管理服务（AWS Secrets Manager、HashiCorp Vault、K8s Secret 等）注入；
- 通过反向代理（Caddy / nginx / Traefik）做 TLS 终结，不要直接暴露 HTTP；
- `evaluate` action 默认受限；仅在受信任规则中开启 `allowEvaluateDOM`；
- 浏览器扩展/userscript 通过组织内部渠道分发，避免公开传播密钥。

## 致谢

我们感谢所有负责任披露安全问题的研究者。
