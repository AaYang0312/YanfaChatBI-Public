# 配置参考

本文件列出后端与前端支持的环境变量、默认值和安全建议。示例文件：

- 后端：`backend/.env.example`
- 前端：`client/.env.example`
- Compose：`docker-compose/.env.example`

> 环境变量在进程启动时读取。修改 `.env` 后需要重启对应服务。

## 1. 变量优先级

| 场景 | 读取顺序 |
| --- | --- |
| 数据存储相关 | `YANFA_*` → 旧名 `NEOBASE_*` → 默认值 |
| 其他变量 | 环境变量 → 默认值 |
| 前端 | 构建时注入，修改后必须重新构建镜像 |

`YANFA_*` 是当前名称。旧部署中的 `NEOBASE_MONGODB_URI`、`NEOBASE_REDIS_HOST` 等仍可工作，但建议尽快迁移。

## 2. 基础服务

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PORT` | `3000` | 后端监听端口 |
| `ENVIRONMENT` | `DEVELOPMENT` | `DEVELOPMENT` / `PRODUCTION` |
| `IS_DOCKER` | `false` | 容器内运行时设为 `true` |
| `CORS_ALLOWED_ORIGIN` | `http://localhost:5173` | 允许的前端来源，多个用逗号分隔 |
| `MAX_CHATS_PER_USER` | `1` | `0` 为试用模式（最多 2 个连接），`1` 不限制 |
| `LANDING_PAGE_CORS_ALLOWED_ORIGIN` | 空 | 可选，独立官网/落地页来源 |

## 3. 认证与加密

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `YANFA_ADMIN_USERNAME` | 内置占位值 | **必须显式设置** |
| `YANFA_ADMIN_PASSWORD` | 内置占位值 | **必须显式设置**，后端拒绝示例默认值 |
| `JWT_SECRET` | 内置弱默认值 | JWT 签名密钥，生产必须替换 |
| `JWT_EXPIRATION_MILLISECONDS` | 10 天 | Access Token 有效期 |
| `JWT_REFRESH_EXPIRATION_MILLISECONDS` | 30 天 | Refresh Token 有效期（兼容旧名 `USER_JWT_*`） |
| `SCHEMA_ENCRYPTION_KEY` | 内置弱默认值 | 数据源凭据、模型 Key 的 AES-GCM 密钥；更换后旧密文无法解密 |
| `SPREADSHEET_DATA_ENCRYPTION_KEY` | 内置弱默认值 | 表格数据加密密钥，**必须恰好 32 个字符** |

生成示例：

```bash
openssl rand -hex 16    # 32 字符，用于 SCHEMA_ENCRYPTION_KEY / SPREADSHEET_DATA_ENCRYPTION_KEY
openssl rand -hex 32    # 64 字符，用于 JWT_SECRET
```

## 4. 应用数据库

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `YANFA_MONGODB_URI` | `mongodb://localhost:27017/yanfachatbi` | 用户、会话、连接、提供商 |
| `YANFA_MONGODB_NAME` | `yanfachatbi` | 数据库名 |
| `YANFA_REDIS_HOST` | `localhost` | 会话与 schema 缓存 |
| `YANFA_REDIS_PORT` | `6379` | |
| `YANFA_REDIS_USERNAME` | 空 | 本地 compose 为 `yanfachatbi` |
| `YANFA_REDIS_PASSWORD` | `default` | 与 `redis.acl` 保持一致 |

## 5. 表格数据 PostgreSQL

CSV / Excel 上传后写入独立的 PostgreSQL，不进入业务数据源。

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `SPREADSHEET_POSTGRES_HOST` | `localhost` | |
| `SPREADSHEET_POSTGRES_PORT` | `5432` | |
| `SPREADSHEET_POSTGRES_DATABASE` | `yanfachatbi_spreadsheet_storage` | |
| `SPREADSHEET_POSTGRES_USERNAME` | `yanfachatbi_spreadsheet` | |
| `SPREADSHEET_POSTGRES_PASSWORD` | 空 | 必填 |
| `SPREADSHEET_POSTGRES_SSL_MODE` | `disable` | `disable` / `require` / `verify-ca` / `verify-full` |

## 6. 模型提供商

### 6.1 启动引导

没有在界面中保存过提供商时，后端会尝试用以下变量创建默认提供商：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `OPENAI_API_KEY` | 空 | 配置后创建默认提供商；为空则模型未配置 |
| `OPENAI_BASE_URL` | 官方地址 | OpenAI 兼容网关地址，可带 `/v1` |
| `DEFAULT_LLM_MODEL` | `gpt-4o` | 引导提供商的默认模型；必须存在于已配置模型列表 |

创建后可在 **更多 → 对话设置 → 模型提供商** 中修改。界面保存的配置优先，不会因环境变量变化被覆盖。

### 6.2 提供商字段

| 字段 | 说明 |
| --- | --- |
| `name` | 显示名称，唯一（不区分大小写） |
| `preset_id` | 预设标识：`openai` / `deepseek` / `zhipu` / `moonshot` / `minimax` / `qwen` / 自定义 |
| `base_url` | OpenAI 兼容地址，必须包含版本路径（如 `/v1`） |
| `api_key` | 加密保存；读取接口只返回掩码 |
| `models[]` | 模型 ID、显示名、上下文长度、最大输出、温度、思考参数 |

### 6.3 思考强度

未手动配置时，后端按模型 ID 自动推断：

| 模型匹配 | 参数 | 档位 |
| --- | --- | --- |
| `gpt-5` / `gpt-6` / `o1` / `o3` / `o4` | `reasoning_effort` | low / medium / high / xhigh / max |
| `kimi-k3` | `reasoning_effort` | low / high / max |
| `minimax-m3` | `reasoning.effort` | low → max |
| `glm-4.7` / `glm-5` | `thinking.type` | enabled / disabled |
| `qwen3` | `enable_thinking` | true / false |
| `deepseek-v4` / `deepseek-reasoner` | `thinking.type` | true / false |

## 7. 嵌入与 RAG

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `EMBEDDING_PROVIDER` | 自动 | 目前仅支持 OpenAI 兼容接口 |
| `EMBEDDING_MODEL` | `text-embedding-3-small` | 常用：`text-embedding-3-small`（1536 维）、`text-embedding-3-large`（3072 维）、`text-embedding-ada-002`（1536 维） |
| `EMBEDDING_API_KEY` | 回退到 `OPENAI_API_KEY` | 独立嵌入服务的 Key |
| `EMBEDDING_BASE_URL` | 回退到 OpenAI 地址 | 独立嵌入服务地址 |
| `QDRANT_HOST` | `localhost` | |
| `QDRANT_PORT` | `6334` | gRPC 端口 |
| `QDRANT_API_KEY` | 空 | Qdrant Cloud 必填 |
| `QDRANT_USE_TLS` | `false` | Qdrant Cloud 设为 `true` |
| `EMBEDDING_SCHEMA_COLLECTION` | `neobase_schema` | 表结构 collection；存量标识，通常不改 |
| `EMBEDDING_MESSAGE_COLLECTION` | `neobase_messages` | 历史消息 collection |
| `EMBEDDING_OBJECT_SCHEMA_COLLECTION` | `<schema>_objects_v2` | 视图对象 collection |

> 嵌入模型维度必须与 collection 一致。更换模型后需要重建 collection 并重新索引，否则检索会失败或返回空结果。

## 8. 上下文压缩与结果回灌

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `CONTEXT_COMPACTION_ENABLED` | `true` | 接近上下文预算时把旧消息折叠为结构化摘要 |
| `CONTEXT_COMPACTION_THRESHOLD` | `0.75` | 达到预算的该比例时触发 |
| `CONTEXT_COMPACTION_KEEP_TURNS` | `6` | 始终保留的最近轮数 |
| `CONTEXT_COMPACTION_MIN_MESSAGES` | `4` | 可折叠消息的最小数量 |
| `CONTEXT_SUMMARY_MAX_TOKENS` | `4000` | 摘要超过该值时二次压缩 |
| `CONTEXT_SUMMARY_MODEL` | 空 | 摘要专用模型；为空时使用会话模型 |
| `RAG_AFTER_COMPACTION_TOP_K` | `3` | 对已折叠历史额外检索的条数 |
| `RESULT_DIGEST_MAX_CHARS` | `4000` | 单条查询结果进入提示词的最大字符数 |
| `RESULT_DIGEST_TOTAL_MAX_CHARS` | `8000` | 一轮对话所有结果摘要的总预算 |

## 9. PostgreSQL 视图查询

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `QUERY_OBJECTS_ENABLED` | `false` | 开启后对象选择器可列出视图，并支持按视图查询 |

开启前请确认：

- 数据源账号对目标视图有 `SELECT` 权限
- 不需要为了查询视图而授予底表权限（视图按自身权限执行）
- 旧聊天继续走原有表路径；显式选择视图的聊天保持视图范围

## 10. Google OAuth 与邮件

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `GOOGLE_CLIENT_ID` | 空 | 同时用于登录与 Google Sheets |
| `GOOGLE_CLIENT_SECRET` | 空 | |
| `GOOGLE_REDIRECT_URL` | `http://localhost:5173/auth/google/callback` | 必须与 Google Console 配置一致 |
| `VITE_GOOGLE_CLIENT_ID` | 空 | 前端 Google 登录按钮 |
| `VITE_GOOGLE_REDIRECT_URI` | 同上 | 前端回调地址 |
| `SMTP_HOST` | 空 | 忘记密码邮件 |
| `SMTP_PORT` | `587` | |
| `SMTP_SECURE` | `false` | `true` 使用隐式 TLS |
| `SMTP_USER` / `SMTP_PASSWORD` | 空 | |
| `SMTP_FROM_NAME` | `YanfaChatBI` | |
| `SMTP_FROM_EMAIL` | 同 `SMTP_USER` | |

不配置 SMTP 时，邮箱相关功能不可用，不影响主流程。

## 11. 前端变量

前端变量在 **构建时** 注入，修改后需要重新执行 `npm run build` 或重建镜像。

| 变量 | 示例 | 说明 |
| --- | --- | --- |
| `VITE_API_URL` | `http://localhost:3000/api` | 后端地址，必须带 `/api` |
| `VITE_FRONTEND_BASE_URL` | `http://localhost:5173/` | 前端对外地址，结尾带 `/` |
| `VITE_ENVIRONMENT` | `DEVELOPMENT` | `DEVELOPMENT` / `PRODUCTION` |
| `VITE_GOOGLE_CLIENT_ID` | | Google 登录 |
| `VITE_GOOGLE_REDIRECT_URI` | | Google 回调 |

生产构建示例：

```bash
VITE_API_URL=https://api.example.com/api \
VITE_FRONTEND_BASE_URL=https://chat.example.com/ \
VITE_ENVIRONMENT=PRODUCTION \
npm run build
```

## 12. 示例：生产环境

```bash
ENVIRONMENT=PRODUCTION
PORT=3000
CORS_ALLOWED_ORIGIN=https://chat.example.com
YANFA_ADMIN_USERNAME=admin
YANFA_ADMIN_PASSWORD=<random-strong-password>
JWT_SECRET=<openssl rand -hex 32>
JWT_EXPIRATION_MILLISECONDS=3600000
JWT_REFRESH_EXPIRATION_MILLISECONDS=604800000
SCHEMA_ENCRYPTION_KEY=<openssl rand -hex 16>
SPREADSHEET_DATA_ENCRYPTION_KEY=<openssl rand -hex 16>

YANFA_MONGODB_URI=mongodb://user:pass@mongo.internal:27017/yanfachatbi?authSource=admin
YANFA_MONGODB_NAME=yanfachatbi
YANFA_REDIS_HOST=redis.internal
YANFA_REDIS_USERNAME=yanfachatbi
YANFA_REDIS_PASSWORD=<redis-password>

SPREADSHEET_POSTGRES_HOST=pg.internal
SPREADSHEET_POSTGRES_PORT=5432
SPREADSHEET_POSTGRES_DATABASE=yanfachatbi_spreadsheet_storage
SPREADSHEET_POSTGRES_USERNAME=yanfachatbi_spreadsheet
SPREADSHEET_POSTGRES_PASSWORD=<pg-password>
SPREADSHEET_POSTGRES_SSL_MODE=require

QDRANT_HOST=qdrant.internal
QDRANT_PORT=6334
QDRANT_API_KEY=<qdrant-key>
QDRANT_USE_TLS=true

EMBEDDING_MODEL=text-embedding-3-small
CONTEXT_COMPACTION_ENABLED=true
QUERY_OBJECTS_ENABLED=false
```

## 13. 相关文档

- [部署指南](../SETUP.md)
- [架构说明](ARCHITECTURE.md)
- [上下文压缩设计](specs/context-compaction.md)
- [模型提供商设计](specs/custom-model-providers.md)
- [视图查询设计](specs/view-query-support.md)
