<!--
感谢你提交 Pull Request！请按下方模板填写，帮助 reviewer 快速理解你的变更。
提交前请确认：
- 已阅读 CONTRIBUTING.md 与 CODE_OF_CONDUCT.md
- 本 PR 关联一个 Issue（若无，请先在 Issue 中讨论）
- CI（Node lint/test、Go test、e2e）全部通过
-->

## 关联 Issue

<!-- 例如：Closes #123 -->

## 变更类型

- [ ] feat：新增功能
- [ ] fix：Bug 修复
- [ ] docs：文档
- [ ] refactor：重构（不改变行为）
- [ ] perf：性能优化
- [ ] test：测试补充
- [ ] build / ci：构建 / CI
- [ ] chore：其他杂项

## 变更说明

<!-- 这段 PR 做了什么、为什么这么做。重点写"为什么"，而不是复述代码。 -->

## 影响范围

- [ ] 服务端（Go）
- [ ] 浏览器扩展
- [ ] Admin UI
- [ ] TypeScript 核心库（rule-engine / scriptcat-engine / worker）
- [ ] DSL schema
- [ ] 文档
- [ ] CI/CD
- [ ] 其他：

## 测试

- [ ] 已运行 `npm run lint`
- [ ] 已运行 `npm test`（vitest + admin-ui）
- [ ] 已运行 `npm run test:coverage`（覆盖率仍 ≥ 90%）
- [ ] 已运行 `cd server && go test ./... && go vet ./... && go build ./...`
- [ ] 已运行 e2e（`npm run test:e2e` / `npm run test:e2e:browser`）
- [ ] 已更新 Swagger（如修改了 API，已运行 `swag init`）

### 测试说明

<!-- 新增了哪些测试？或手动验证了哪些场景？ -->

## 破坏性变更

- [ ] 本 PR 不包含破坏性变更
- [ ] 本 PR 包含破坏性变更（请在下方说明迁移路径，并在 PR 标题加 `!`，例如 `feat(scheduler)!: ...`）

## 检查清单

- [ ] 代码遵循项目的风格指南（Go: gofmt；TS: tsc 通过；2 空格缩进）
- [ ] 新功能附带测试，修复 Bug 附带回归测试
- [ ] 没有引入任何真实 API key、密码或密钥（测试 fixture 除外）
- [ ] 文档已同步更新（README / docs/ / Swagger）
- [ ] Commit 遵循 Conventional Commits
- [ ] 本 PR 不包含 console.log / fmt.Println 等调试残留

## 截图 / 录屏（可选）

<!-- 如果修改了 UI 或交互行为，请补充截图或录屏。 -->

## 其他说明

<!-- 任何 reviewer 需要额外关注的信息。 -->
