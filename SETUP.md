# YanfaChatBI 部署与安装指南

本指南覆盖本地启动、生产部署、模型接入与常见故障处理。只想快速跑起来，直接看 [Docker Compose 启动](#1-docker-compose-启动推荐)。

## 目录

- [1. Docker Compose 启动（推荐）](#1-docker-compose-启动推荐)
- [2. 首次登录与用户](#2-首次登录与用户)
- [3. 本地开发](#3-本地开发)
- [4. 服务器部署](#4-服务器部署)
- [5. 配置模型](#5-配置模型)
- [6. 数据持久化与升级](#6-数据持久化与升级)
- [7. 故障排查](#7-故障排查)
- [8. 测试命令](#8-测试命令)

---

## 1. Docker Compose 启动（推荐）

### 1.1 环境要求

| 组件 | 版本 | 用途 |
| --- | --- | --- |
| Docker | 24+ | 运行全部服务 |
| Docker Compose | v2 | 编排 |
| Go | 1.24+ | 仅本地开发 |
| Node.js | 20+ | 仅本地开发 |

服务器最低建议 2 vCPU / 4 GB 内存；启用 Qdrant 与嵌入索引后建议 4 vCPU / 8 GB。

### 1.2 准备配置

```bash
cd docker-compose
cp .env.example .env
```

至少修改以下四项：

```bash
YANFA_ADMIN_USERNAME=your-admin
YANFA_ADMIN_PASSWORD='<strong-password>'

# 数据源凭据加密密钥，任意非空字符串；建议 32 字节随机值
SCHEMA_ENCRYPTION_KEY="$(openssl rand -hex 16)"

# JWT 签名密钥，越长越好
JWT_SECRET="$(openssl rand -hex 32)"

# 表格数据加密密钥：必须恰好 32 个字符
SPREADSHEET_DATA_ENCRYPTION_KEY="$(openssl rand -hex 16)"
```

> `SPREADSHEET_DATA_ENCRYPTION_KEY` 长度不是 32 时后端会拒绝启动。密钥一旦用于加密数据，请勿随意更换，否则历史表格与已保存的模型 Key 将无法解密。

其余变量按需填写，完整说明见 [配置参考](docs/CONFIGURATION.md)。

### 1.3 启动

```bash
# 编译网络在 compose 中声明为 external，需要先创建
docker network create yanfachatbi-network

docker compose -f docker-compose-local.yml up -d --build
docker compose -f docker-compose-local.yml ps
```

首次构建需要下载依赖与编译前后端，请预留几分钟。查看日志：

```bash
docker compose -f docker-compose-local.yml logs -f yanfachatbi-backend
```

健康检查通过即表示后端就绪：

```bash
curl -s http://localhost:3000/health
```

### 1.4 服务与端口

| 服务 | 容器 | 端口 |
| --- | --- | --- |
| 前端 | `yanfachatbi-client` | 5173 |
| 后端 | `yanfachatbi-backend` | 3000 |
| MongoDB | `yanfachatbi-mongodb` | 27017 |
| Redis | `yanfachatbi-redis` | 6379 |
| 表格存储 PostgreSQL | `yanfachatbi-spreadsheet-postgres` | 5433 → 容器 5432 |
| Qdrant | `yanfachatbi-qdrant` | 6333（REST）/ 6334（gRPC） |

### 1.5 只启动依赖（本地开发）

如果要在宿主机跑 Go / Vite：

```bash
docker network create yanfachatbi-network
docker compose -f docker-compose-dependencies.yml up -d
```

它会启动 MongoDB、Redis、表格 PostgreSQL、Qdrant，后端与前端由你手动运行。

---

## 2. 首次登录与用户

1. 打开 `http://localhost:5173`
2. 使用 `YANFA_ADMIN_USERNAME` / `YANFA_ADMIN_PASSWORD` 登录
3. 普通用户不能自助注册，需要管理员生成注册密钥：

```bash
curl -X POST http://localhost:3000/api/auth/generate-signup-secret \
  -H 'Content-Type: application/json' \
  -d '{"username":"<admin>","password":"<admin-password>"}'
```

响应中的 `data.secret` 即注册密钥。登录页选择「注册」，填写用户名、邮箱、密码（至少 6 位）和注册密钥。

### 管理员能做什么

- 生成注册密钥
- 数据同步操作员授权（同步任务的启动与维护）
- 查看与管理全部数据源连接和模型提供商

> 不要在生产环境保留示例密码。后端会拒绝 `yanfachatbi-admin` / `yanfachatbi-password` 这组默认值。

---

## 3. 本地开发

### 3.1 后端

```bash
cd backend
cp .env.example .env
# 修改数据库地址为 localhost，并按需填写 OPENAI_API_KEY / QDRANT_HOST
go run ./cmd
```

关键本地配置：

```bash
PORT=3000
IS_DOCKER=false
ENVIRONMENT=DEVELOPMENT
CORS_ALLOWED_ORIGIN=http://localhost:5173
YANFA_MONGODB_URI=mongodb://yanfachatbi:yanfachatbi@localhost:27017/yanfachatbi?authSource=admin
YANFA_MONGODB_NAME=yanfachatbi
YANFA_REDIS_HOST=localhost
YANFA_REDIS_USERNAME=yanfachatbi
YANFA_REDIS_PASSWORD=default
SPREADSHEET_POSTGRES_HOST=localhost
SPREADSHEET_POSTGRES_PORT=5433
SPREADSHEET_POSTGRES_DATABASE=yanfachatbi_spreadsheet_storage
SPREADSHEET_POSTGRES_USERNAME=yanfachatbi
SPREADSHEET_POSTGRES_PASSWORD=yanfachatbi
SPREADSHEET_DATA_ENCRYPTION_KEY=<32 chars>
QDRANT_HOST=localhost
QDRANT_PORT=6334
QDRANT_API_KEY=yanfachatbi-qdrant-local-key
```

启动后访问 `http://localhost:3000/health` 验证。

### 3.2 前端

```bash
cd client
npm ci
cp .env.example .env
npm run dev
```

`client/.env`：

```bash
VITE_API_URL=http://localhost:3000/api
VITE_FRONTEND_BASE_URL=http://localhost:5173/
VITE_ENVIRONMENT=DEVELOPMENT
```

### 3.3 目录约定

| 路径 | 说明 |
| --- | --- |
| `backend/cmd` | API 入口 |
| `backend/cmd/erp-*` | ERP 同步、维护、源注册命令 |
| `backend/internal/services` | 问数、压缩、可视化、助手、同步业务 |
| `backend/pkg/llm` | OpenAI 兼容客户端与工具定义 |
| `backend/pkg/dbmanager` | 各数据库方言、执行器与只读判定 |
| `client/src/components` | 聊天、看板、设置等界面 |
| `client/src/i18n` | 中文文案 |
| `docs/specs` | 功能设计文档 |

---

## 4. 服务器部署

### 4.1 Docker Compose（服务器版）

`docker-compose-server.yml` 会构建前后端镜像，但复用外部依赖（MongoDB / Redis / Qdrant / 表格 PostgreSQL）：

```bash
# 在服务器仓库目录
docker network create yanfachatbi-network   # 若尚未创建

cd docker-compose
cp .env.example .env
# 填写生产密钥、域名与外部依赖地址
docker compose -f docker-compose-server.yml up -d --build
```

生产环境必须调整：

- `ENVIRONMENT=PRODUCTION`
- `CORS_ALLOWED_ORIGIN=https://<你的前端域名>`
- `VITE_API_URL=https://<你的后端域名>/api`
- `VITE_FRONTEND_BASE_URL=https://<你的前端域名>/`
- `YANFA_MONGODB_URI` / `YANFA_REDIS_*` 指向受管实例或内网地址

### 4.2 反向代理与 HTTPS

前端是静态 Vite 构建，后端是 HTTP API + SSE。Nginx 示例：

```nginx
server {
    listen 443 ssl http2;
    server_name chat.example.com;

    ssl_certificate     /etc/letsencrypt/live/chat.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/chat.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:5173;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location /api/ {
        proxy_pass http://127.0.0.1:3000;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header Connection '';
        proxy_buffering off;          # SSE 必需
        proxy_read_timeout 3600s;
    }
}
```

> SSE 流式回答依赖 `proxy_buffering off` 与足够的 `proxy_read_timeout`，否则会出现「回答一次性出现」或中途断开。

### 4.3 Dokploy

仓库提供 `docker-compose-dokploy.yml`。在 Dokploy 中新建 Compose 项目，指向该文件并配置环境变量即可；数据库可使用 Dokploy 的托管服务，也可以复用外部实例。

### 4.4 上线前检查

- [ ] 管理员密码、`JWT_SECRET`、`SCHEMA_ENCRYPTION_KEY`、`SPREADSHEET_DATA_ENCRYPTION_KEY` 均已替换为随机值
- [ ] `ENVIRONMENT=PRODUCTION`
- [ ] CORS 只允许实际前端域名
- [ ] 反向代理关闭 SSE 缓冲
- [ ] MongoDB / Redis / Qdrant 未直接暴露公网
- [ ] 已配置数据库备份
- [ ] 模型 Key 通过界面录入，不写进仓库

---

## 5. 配置模型

平台只支持 OpenAI 兼容接口。推荐在界面中配置：

1. 登录 → **更多 → 对话设置 → 模型提供商**
2. 选择预设（OpenAI / DeepSeek / 智谱 GLM / Moonshot Kimi / MiniMax / 通义千问）或「自定义」
3. 填写 Base URL、API Key
4. 点击「测试连接」，拉取模型列表并勾选需要的模型
5. 保存后回到聊天框，在输入框上方切换模型与思考强度

模型 Key 使用 AES-GCM 加密存储，列表接口只返回掩码。

### 5.1 环境变量引导（可选）

也可以在启动时用环境变量创建首个提供商：

```bash
OPENAI_API_KEY=sk-...
DEFAULT_LLM_MODEL=gpt-4o
OPENAI_BASE_URL=https://api.openai.com/v1
```

适合自动化部署；之后仍可在界面中修改。

### 5.2 嵌入模型

RAG 依赖嵌入模型，必须与 Qdrant collection 的维度一致：

```bash
EMBEDDING_PROVIDER=openai
EMBEDDING_MODEL=text-embedding-3-small   # 1536 维
```

更换嵌入模型（尤其是维度变化）后，需要重建 `neobase_schema` / `neobase_messages`（或自定义名称）collection，并重新索引 schema 与历史消息。collection 名称是存量数据标识，默认值保持不变。

### 5.3 思考强度

模型是否支持思考、参数名与可选档位由模型 ID 自动推断：

| 模型 | 参数 |
| --- | --- |
| GPT-5 / GPT-6 / o1 / o3 / o4 | `reasoning_effort`：low → max |
| Kimi K3 | `reasoning_effort`：low / high / max |
| MiniMax M3 | `reasoning.effort` |
| GLM-4.7 / GLM-5 | `thinking.type`：enabled / disabled |
| Qwen3 | `enable_thinking` |
| DeepSeek V4 / reasoner | `thinking.type` |

未知模型不显示滑块，按提供商默认行为执行。也可以在模型配置中手动覆盖思考参数。

---

## 6. 数据持久化与升级

### 6.1 数据卷

| 卷 | 内容 |
| --- | --- |
| `yanfachatbi-mongodb-data` | 用户、会话、连接、模型提供商、任务 |
| `yanfachatbi-redis-data` | 会话与缓存（可重建） |
| `yanfachatbi-spreadsheet-postgres-data` | 上传的 CSV / Excel 数据 |
| `yanfachatbi-qdrant-data` | schema / message 向量索引 |

### 6.2 备份

```bash
# MongoDB
docker exec yanfachatbi-mongodb mongodump \
  --username yanfachatbi --password yanfachatbi \
  --authenticationDatabase admin --archive=/tmp/backup.gz --gzip
docker cp yanfachatbi-mongodb:/tmp/backup.gz ./mongodb-$(date +%F).gz

# 表格 PostgreSQL
docker exec yanfachatbi-spreadsheet-postgres pg_dump \
  -U yanfachatbi yanfachatbi_spreadsheet_storage | gzip > spreadsheet-$(date +%F).sql.gz
```

Qdrant 数据可由 schema 与历史消息重新索引，但重建耗时较长，建议与 MongoDB 一起备份。

### 6.3 升级

```bash
git pull
docker compose -f docker-compose/docker-compose-local.yml up -d --build
```

升级前先备份 MongoDB。若版本说明要求重建向量索引，删除对应 Qdrant collection 后重新触发索引。

> 环境变量已从 `NEOBASE_*` 迁移为 `YANFA_*`。新版本仍兼容旧变量名，但建议在升级时同步改名，避免两套变量并存。

---

## 7. 故障排查

| 现象 | 排查 |
| --- | --- |
| 前端能打开但接口 502 | `docker compose ps` 看后端是否健康；`curl localhost:3000/health` |
| 登录后立刻掉线 | 检查 `JWT_SECRET`、`USER_JWT_EXPIRATION_MILLISECONDS` 与系统时间 |
| 浏览器报 CORS | `CORS_ALLOWED_ORIGIN` 必须与实际访问地址完全一致（协议、域名、端口） |
| 后端启动报解密失败 | `SCHEMA_ENCRYPTION_KEY` / `SPREADSHEET_DATA_ENCRYPTION_KEY` 被更换；恢复原密钥 |
| 后端启动报 `SPREADSHEET_DATA_ENCRYPTION_KEY must be exactly 32 bytes` | 生成 `openssl rand -hex 16`（32 个字符） |
| Redis 连接失败 | 检查 `redis.acl` 用户名是否正确，容器内 ACL 文件是否挂载 |
| 模型「测试连接」失败 | 核对 Base URL 是否包含 `/v1`；部分网关需要 `/v4` 等路径；检查 Key 与出网策略 |
| 模型返回 404 / 模型不存在 | 刷新模型列表后重新选择；确认提供商与模型 ID 匹配 |
| 回答没有图表 / 解读 | 会话说设置中开启「自动执行」「自动生成可视化」「自动解读结果」；仅生成模式不会执行查询 |
| 长对话报上下文超限 | 确认 `CONTEXT_COMPACTION_ENABLED=true`，或减小 `CONTEXT_COMPACTION_THRESHOLD` |
| 视图看不到 | 默认只列普通表；开启 `QUERY_OBJECTS_ENABLED=true` 并在对象选择中显式选择视图 |
| 同步按钮提示无权限 | 管理员需把账号加入同步操作员；见 [ERP 增量同步](docs/erp-sync.md) |
| 上传 CSV 失败 | 检查表格 PostgreSQL 是否可连、`SPREADSHEET_POSTGRES_*` 与加密密钥是否一致 |

查看日志：

```bash
docker compose -f docker-compose/docker-compose-local.yml logs -f --tail=200 yanfachatbi-backend
docker compose -f docker-compose/docker-compose-local.yml logs -f --tail=200 yanfachatbi-client
```

---

## 8. 测试命令

```bash
# 后端
cd backend
go build ./...
go test ./...

# 前端
cd client
npx tsc --noEmit
npm test
npm run test:browser
```

说明：

- `pkg/sourcegate` 使用 `//go:build linux`，Windows 上直接 `go test ./...` 会跳过/报无构建文件，请在 Linux、WSL 或容器内执行完整测试。
- 浏览器测试需要先启动前端与后端，或使用仓库中的 mock 模式，具体见 `client/playwright.config.ts`。

---

## 相关文档

- [配置参考](docs/CONFIGURATION.md)
- [架构说明](docs/ARCHITECTURE.md)
- [贡献指南](CONTRIBUTING.md)
- [安全说明](SECURITY.md)
- [ERP 增量同步](docs/erp-sync.md)
