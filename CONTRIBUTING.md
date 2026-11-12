# 贡献指南

感谢你愿意改进 YanfaChatBI。本文说明本地开发、提交规范与 PR 检查项。

## 1. 开始之前

- 先搜索现有 Issue，避免重复。
- 较大的功能或架构调整，请先开 Issue 讨论方案，再开始实现。
- 安全问题不要公开提交 Issue，见 [SECURITY.md](SECURITY.md)。

## 2. 本地开发

```bash
git clone <your-fork-url> YanfaChatBI
cd YanfaChatBI

# 依赖服务
docker network create yanfachatbi-network
docker compose -f docker-compose/docker-compose-dependencies.yml up -d

# 后端
cd backend
cp .env.example .env
go run ./cmd

# 前端（新终端）
cd client
npm ci
cp .env.example .env
npm run dev
```

详细配置见 [SETUP.md](SETUP.md) 与 [配置参考](docs/CONFIGURATION.md)。

## 3. 分支与提交

从 `main` 切出短生命周期分支：

```bash
git checkout -b feat/view-picker
git checkout -b fix/sse-reconnect
git checkout -b docs/deployment
```

提交信息使用 `type(scope): summary`：

```text
feat(chat): support view objects in the picker
fix(provider): refresh models after base URL change
docs(setup): add nginx SSE example
test(compaction): cover revision conflict
chore(deps): bump qdrant client
```

要求：

- 一个 PR 只做一件事；格式化、重命名与功能改动尽量分开。
- 不提交 `.env`、密钥、数据库导出、构建产物或 `.agent-runs/`。
- 涉及行为变化时同步更新文档与测试。

## 4. 代码约定

### 后端（Go）

- 使用 `gofmt`；提交前执行 `go build ./...`。
- Handler 不直接访问数据库；业务逻辑放在 `internal/services`。
- 新增跨包依赖时优先定义接口，避免 `pkg/*` 反向依赖 `internal/*`。
- 数据源相关改动必须考虑：只读判定、超时/取消、权限边界、审计。
- 不要扩大数据源账号权限来“让测试通过”。

### 前端（TypeScript / React）

- 执行 `npm run lint` 与 `npx tsc --noEmit`。
- 品牌名从 `client/src/i18n/zh-CN.ts` 的 `productName` 读取，不硬编码。
- 用户可见文案统一放入 `i18n`；当前界面语言为简体中文。
- 组件保持现有 Neo Brutalism 风格：粗描边、硬阴影、高对比配色。

## 5. 测试要求

| 改动 | 最低验证 |
| --- | --- |
| 后端逻辑 | `go test ./...`（Linux / WSL / 容器） |
| 前端组件 | `npm test` |
| 交互流程 | `npm run test:browser` |
| 数据源 / 查询 | 针对该数据源的集成测试或 fixture |
| 模型提供商 | 连接测试 + 思考参数注入测试 |
| 文档 | 链接可访问、命令可复制执行 |

> `pkg/sourcegate` 使用 Linux 构建标签。Windows 上完整测试请使用 WSL 或 Docker。

## 6. 新增数据库或模型

- 数据库：[docs/HOW-TO-IMPLEMENT-NEW-DB.md](docs/HOW-TO-IMPLEMENT-NEW-DB.md)
- 模型提供商：[docs/HOW-TO-IMPLEMENT-NEW-LLM.md](docs/HOW-TO-IMPLEMENT-NEW-LLM.md)

新增数据库时至少覆盖：连接、schema 读取、查询执行、只读分类、前端连接表单与文档。

## 7. Pull Request 清单

提交 PR 前确认：

- [ ] 分支基于最新 `main`，无无关改动
- [ ] 已补充或更新测试
- [ ] `go build ./...`、`go test ./...`、`npm test`、`npx tsc --noEmit` 通过
- [ ] 涉及界面时附截图或录屏
- [ ] 涉及配置时更新 `.env.example` 与 `docs/CONFIGURATION.md`
- [ ] 涉及数据库/权限时说明迁移与回滚方式
- [ ] 没有提交密钥、个人数据或生产连接串
- [ ] PR 描述写清：背景、改动、验证方式、风险

## 8. 评审与合并

- 维护者会优先检查正确性、权限边界与回归风险。
- 审查意见会尽量给出文件与行号；请逐项回复或修改。
- 合并方式以 Squash Merge 为主，保持 `main` 历史清晰。

## 9. 社区约定

请保持尊重与建设性。人身攻击、歧视性言论、泄露他人隐私或生产数据的行为不会被接受。
