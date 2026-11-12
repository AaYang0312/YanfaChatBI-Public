# 自定义模型 Provider 设置 技术 Spec

状态：已实现（2026-11-11，commit `8b3a026`，branch `codex/model-providers-20261111`；D1–D7 全部落地）
日期：2026-11-11
基线：`eb67849`（codex/view-query-support-20261111）

## 1. 目标与范围

### 1.1 交付目标

1. **设置入口**：管理员在 UI 中管理模型 provider——新增、编辑、停用、删除。
2. **自定义 provider**：任意 OpenAI-compatible 端点（base URL + API key + 模型列表），无需改代码或重启。
3. **自带常用 provider**：内置预设列表（6 个），一键添加，填 key 即用。
4. **模型列表自动/手动刷新**：填写 key 后可一键从端点拉取模型列表，也可手动维护；保存后自动尝试刷新一次。
5. **思考强度支持**：每个模型可声明是否支持思考强度（reasoning effort / thinking）及其档位与请求参数；聊天时按模型能力选择档位。
6. **删除遗留实现**：移除 Gemini（Google）、Claude、Ollama 的 LLM provider 实现，以及 Gemini embedding、相关配置、模型清单、前端选项。

### 1.2 必要范围

- 后端 provider 配置持久化（MongoDB），运行时生效，无需重启。
- API key 加密存储，接口返回脱敏。
- 模型清单由 provider 配置聚合，`/api/llm-models` 与前端模型下拉改为读配置。
- 管理员权限控制；普通用户只读模型列表。
- 现有 `OPENAI_*` 环境变量作为首次启动的 bootstrap 配置，保证升级不中断。
- 模型列表拉取失败时允许手动填写，不阻塞保存。
- 思考强度按模型声明注入请求，未声明时完全不发送该参数。

### 1.3 非目标

- 不实现多租户 provider 隔离（单部署、管理员统一配置；用户级 provider 不在本期）。
- 不实现 Anthropic/Gemini 原生协议适配（如需使用，走其 OpenAI-compatible 端点或第三方网关）。
- 不实现自动模型能力探测（上下文长度、思考强度支持等由配置项声明）。
- 不解析或展示模型的思维链内容（`reasoning_content` 仍按现有响应解析逻辑处理，不新增 UI）。
- 不改变 embedding 的 collection 与维度策略（见 §8 与 D3）。

## 2. 现状与代码依据

### 2.1 LLM 后端

| 事实 | 位置 |
| --- | --- |
| 四套独立 provider 实现 | `backend/pkg/llm/openai.go`(711)、`gemini.go`(875)、`claude.go`(1024)、`ollama.go`(886) |
| Manager 按 provider 字符串 switch 创建 | `backend/pkg/llm/manager.go:29-40` |
| 统一接口 | `backend/pkg/llm/types.go:14-27`（`Client` interface） |
| 配置结构已含 BaseURL 覆盖 | `backend/pkg/llm/types.go:36-44`（`Config.BaseURL`） |
| 模型清单硬编码 | `backend/internal/constants/supported_models.go:35-40`（拼接四个 provider 数组） |
| provider 常量 | `backend/internal/constants/llms.go:8-13` |
| 按 env key 注册客户端 | `backend/internal/di/modules.go:246-500`（四段注册，每段重复 8 个 DBConfig） |
| 模型清单 API 按 env key 过滤 | `backend/internal/apis/handlers/llm_models_handler.go:24`、`routes/llm_models.go:16-32` |
| provider 配置全部来自环境变量 | `backend/config/env_values.go`：`OPENAI_API_KEY/BASE_URL`、`GEMINI_API_KEY`、`CLAUDE_API_KEY`、`OLLAMA_BASE_URL`、`DEFAULT_LLM_MODEL` |
| 无运行时可变配置 | 上述 env 仅在启动时读取 |
| 管理员判定 | `backend/internal/services/auth_service.go:172`（`config.Env.AdminUser`，来自 `YANFA_ADMIN_USERNAME`） |
| 加密工具 | `backend/internal/utils/aes_gcm.go:19-39`（`NewFromConfig` / `Encrypt` / `Decrypt`） |
| **无思考强度支持** | 全仓 `grep -niE "reasoning\|thinking\|effort"` 无业务代码命中；T46 仅做模型清单/端点选择 |

### 2.2 Embedding 现状（D3 评估依据）

| 事实 | 位置 |
| --- | --- |
| 两套 embedding 实现 | `backend/pkg/embedding/openai.go`(124)、`gemini.go`(262) |
| provider 自动检测顺序 OpenAI > Gemini | `backend/internal/constants/embedding.go:311-341` |
| 维度由 provider 决定，创建 collection 时使用 | `backend/internal/services/vectorization_service.go:108-124`（`GetDimension` + `EnsureSchemaCollection/EnsureMessageCollection`） |
| 向量 collection 名来自配置 | `backend/internal/constants`（`SchemaCollectionName`/`MessageCollectionName`），生产实际为 `yanfachatbi_schema_flash_20261110_ec599c3`、`yanfachatbi_messages_flash_20261110_ec599c3` |
| RAG 检索读取两套 collection | `vectorization_service.go:448-464`（schema/hybrid search）、`:758`（message search） |

**生产实际生效配置**（2026-11-11 核实，来自 systemd drop-in 后加载覆盖 `runtime.env`）：

```
EMBEDDING_PROVIDER=openai
EMBEDDING_MODEL=qwen3.7-text-embedding-flash
EMBEDDING_BASE_URL=https://llm-k2ypqdjozd4yspxz.cn-beijing.maas.aliyuncs.com/compatible-mode/v1
DEFAULT_LLM_MODEL=glm-5.3-flash
OPENAI_BASE_URL=https://open.bigmodel.cn/api/paas/v4
```

`runtime.env` 中残留的 `EMBEDDING_PROVIDER="gemini"`、`EMBEDDING_MODEL="gemini-embedding-001"`、`GEMINI_API_KEY` 已被 drop-in 覆盖，**当前 RAG 不依赖 Gemini embedding**。

**结论**：删除 `pkg/embedding/gemini.go` 不影响当前生产 RAG。需要处理两件事：
1. 升级说明提示：若其他部署显式配置 `EMBEDDING_PROVIDER=gemini`，升级后启动会报 "unsupported provider"，需改为 OpenAI-compatible。
2. 部署新版本时清理 `runtime.env` 中的 gemini 残留键，避免 drop-in 变动后误用。

### 2.3 前端

| 事实 | 位置 |
| --- | --- |
| 模型清单由 API 拉取 | `client/src/App.tsx:72`（`llmModels` state） |
| 模型下拉按 provider 分组 | `client/src/components/chat/MessageInput.tsx:57-69`、`:278-310` |
| 聊天级模型选择 | `client/src/types/chat.ts:98`（`preferred_llm_model`） |
| 无全局设置页 | `client/src/App.tsx:501-504` 仅 `/`、`/chat/:id`、OAuth 回调 |
| 现有设置仅在连接模态框内 | `client/src/components/modals/components/SettingsTab.tsx`（连接/聊天设置，非全局） |

### 2.4 结论

- 当前 provider 是"编译期 + 环境变量"决定，UI 无法新增；模型清单与 provider 实现强耦合。
- 删除三个专用实现后，OpenAI-compatible 是唯一协议，正好支撑"自定义 provider"。
- 现有 `Config.BaseURL` 与 `openai.go` 的 HTTP 调用已足够覆盖任意兼容端点。
- 生产 embedding 已是 OpenAI-compatible，删除 Gemini embedding 风险可控。
- 思考强度需要从零实现：模型声明 + 请求注入 + 聊天级选择。

## 3. 方案取舍

| 方案 | 内容 | 结论 |
| --- | --- | --- |
| A. 保留四套实现 + UI 配置 | 在现有 switch 上把 env 换成 DB | 否：与删除目标矛盾，重复代码与维护成本保留 |
| B. 统一 OpenAI-compatible + DB 配置 | 只保留一个客户端；provider 全部由 DB 描述；预设只是前端模板 | **推荐（已采纳）** |
| C. 仅扩展环境变量 | 用多个 `*_API_KEY` 支撑更多 provider | 否：无法满足"设置入口"，每次变更需重启 |

方案 B 的核心假设：所有目标 provider 都提供 OpenAI-compatible `/v1/chat/completions`（OpenAI、DeepSeek、智谱、Moonshot、MiniMax、通义均满足）。

## 4. 数据模型

MongoDB 新集合 `model_providers`：

```jsonc
{
  "_id": ObjectID,
  "name": "DeepSeek",                    // 展示名，唯一
  "slug": "deepseek",                    // 稳定标识，用作 provider 分组 key 与模型前缀
  "base_url": "https://api.deepseek.com/v1",
  "api_key_encrypted": "<AES-GCM>",      // 不落明文；空值表示未配置
  "models": [
    {
      "id": "deepseek-chat",
      "display_name": "DeepSeek Chat",
      "max_completion_tokens": 8192,
      "context_limit": 65536,
      "temperature": 1.0,
      /** 思考强度声明；缺省/unsupported 时不发送任何相关参数 */
      "reasoning": {
        "mode": "effort",                // effort | toggle | custom | none
        "param": "reasoning_effort",     // effort/custom 模式下的请求字段名，支持点路径 "thinking.type"
        "levels": ["low", "medium", "high"], // effort 模式的档位值；toggle 模式为 ["on","off"]
        "default_level": "medium",
        "on_value": true,                // toggle/custom 模式的启用值（JSON 值）
        "off_value": false               // toggle/custom 模式的停用值
      }
    }
  ],
  "enabled": true,
  "preset_id": "deepseek",               // 来自预设时为预设 ID，自定义为 null
  "models_refreshed_at": "2026-11-11T...", // 最近一次从端点拉取模型的时间
  "created_at": "...", "updated_at": "..."
}
```

索引：
- `{name: 1}` 唯一（大小写不敏感比较在服务层做）
- `{enabled: 1}`

约定：
- `slug` 生成后不可变，避免历史 `preferred_llm_model` 归属漂移；
- 模型对外 `id` 使用 `slug/modelId` 复合格式（见 D5），provider 归属无歧义；
- `api_key_encrypted` 使用 `utils.NewFromConfig()`（`SCHEMA_ENCRYPTION_KEY`）加密，与连接凭据同一密钥体系；
- `reasoning` 为可选对象，省略即"不支持思考强度"。

### 4.1 思考强度语义

| mode | 请求注入 | 适用 |
| --- | --- | --- |
| `effort` | `{param: level}`，如 `{"reasoning_effort":"high"}` | OpenAI GPT-6/GPT-5.6、Kimi K3 等支持顶层 `reasoning_effort` 的端点 |
| `toggle` | 按 `param`（可点路径）写 `on_value`/`off_value`，如 `{"thinking":{"type":"enabled"}}` 或 `{"enable_thinking":true}` | 智谱、通义、DeepSeek 等开关式端点 |
| `custom` | 按 `param` 点路径写当前 `level` 值，如 `{"reasoning":{"effort":"max"}}` | MiniMax `reasoning.effort` 等嵌套档位端点 |
| `none` / 缺省 | 不发送任何字段 | 不支持或不需要的模型 |

档位选择：
- `levels` 为允许值，前端只展示这些档位；
- `default_level` 在用户未选择时使用；
- 用户选择"默认"时不发送参数（使用服务端模型默认），选择具体档位时才注入——避免给不支持默认值的端点传参。

### 4.2 聊天级字段

`models.Chat` 新增：

```go
PreferredReasoningLevel string `bson:"preferred_reasoning_level,omitempty" json:"preferred_reasoning_level,omitempty"`
```

取值：空字符串（默认）或 `levels` 中的值。与 `preferred_llm_model` 并列，由聊天设置接口保存。

## 5. 内置预设（D1，已确认）

| 预设 | base URL | 建议默认模型 | 建议 reasoning |
| --- | --- | --- | --- |
| OpenAI | `https://api.openai.com/v1` | `gpt-6-astra`（旗舰）；稳定备选 `gpt-5.6-sol` | `effort`，`reasoning_effort`，levels `["low","medium","high","xhigh","max"]`，默认 `medium` |
| DeepSeek | `https://api.deepseek.com/v1` | `deepseek-v4-flash`（对话/工具）；最难推理用 `deepseek-v4-pro` | `toggle`，`thinking.type`（字段名以官方文档为准），`["on","off"]` |
| 智谱 GLM | `https://open.bigmodel.cn/api/paas/v4` | `glm-5.3`（旗舰）；轻量 `glm-5.3-flash` | `toggle`，`thinking.type`；GLM-5.3 默认开启且不可关闭，三档 Low/High/Max 预算按官方参数名配置（`effort` 或 `thinking.budget`，以文档为准） |
| Moonshot Kimi | `https://api.moonshot.cn/v1` | `kimi-k3`（旗舰，1M 上下文） | `effort`，`reasoning_effort`，levels `["low","high","max"]`，默认 `max` |
| MiniMax | `https://api.minimax.io/v1` | `MiniMax-M3.1-Flash`（1M 上下文，可调思考深度）；`MiniMax-M3` | `custom`，param `reasoning.effort`，levels `["low","medium","high","xhigh","max"]`，默认 `max`；M3 默认关闭思考 |
| 通义千问 | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `qwen3.8-max`（默认混合思考）；轻量 `qwen3.7-flash` | `toggle`，`enable_thinking`，`["on","off"]`；可选 `thinking_budget`（custom） |

调研来源与时效（2026-11-11 检索，均为官方文档或官方发布页）：

- OpenAI：[platform.openai.com/docs/models](https://platform.openai.com/docs/models)（GPT-6 Astra 与 GPT-5.6 家族的 reasoning levels）
- DeepSeek：[api-docs.deepseek.com](https://api-docs.deepseek.com)（`deepseek-chat`/`deepseek-reasoner` 已于 2026-07-24 弃用，当前为 v4 系列）
- 智谱：[docs.bigmodel.cn 思考模式](https://docs.bigmodel.cn/cn/guide/capabilities/thinking-mode)、[GLM-5 模型页](https://docs.bigmodel.cn/cn/guide/models/text/glm-5)
- Moonshot：[platform.kimi.ai thinking models](https://platform.kimi.ai/docs/guide/use-thinking-models)（kimi-k3 支持 `reasoning_effort`）
- MiniMax：[platform.minimax.io 模型调用](https://platform.minimax.io/docs/guides/text-generation)（M3.1-Flash 用 `effort`，M3 用 `reasoning.effort`）
- 通义：[DashScope API 参考](https://help.aliyun.com/zh/model-studio/qwen-api-via-dashscope)、[深度思考文档](https://www.alibabacloud.com/help/en/model-studio/deep-thinking)

注意：模型 ID 与参数名可能随官方调整。预设只是初始占位，管理员可通过"获取模型列表"（`/probe`）按端点实际返回纠正，不会因为预设过期而阻塞使用。

预设仅为前端模板（名称、base URL、推荐模型占位、建议 reasoning、文档链接），添加后写为普通 provider 记录，之后与自定义 provider 完全同权。预设不内置 API key，也不预置可用状态。**不包含 OpenRouter / SiliconFlow / 火山方舟**（用户已明确排除）。

## 6. 后端设计

### 6.1 LLM 客户端

- 删除 `pkg/llm/gemini.go`、`claude.go`、`ollama.go`。
- `openai.go` 作为唯一实现，语义改为 "OpenAI-compatible"。
- `Config.Provider` 改为 `Config.ProviderSlug`（仅用于日志与 `ModelInfo.Provider`），不再决定协议分支。
- `Config` 新增 `Reasoning *ReasoningSpec` 与 `ReasoningLevel string`；请求构造时按 §4.1 注入。
- `manager.go`：`RegisterClient` 改为按 slug 注册；不再 switch provider 协议。
- `GetLLMResponseSchema` / `GetSystemPrompt` 删除 provider 分支，统一使用 OpenAI 响应 schema。

### 6.2 配置服务

新增 `internal/services/model_provider_service.go`：

- `List(ctx)` / `Create` / `Update` / `Delete`
- `Probe(ctx, baseURL, apiKey)`：无需先保存，用于"测试连接/拉取模型"；对 `{base_url}/models` 发起 GET（带 key，超时 10s），返回模型 ID 列表；失败返回脱敏错误。
- `RefreshModels(ctx, id)`：对已保存 provider 重新拉取，合并/更新 `models` 与 `models_refreshed_at`；拉取失败保留旧列表并返回错误。
- 校验：name/slug 唯一；base URL 必须 `http(s)`；models 至少 1 项；key 非空（本地端点见 D6）；`reasoning.mode` 合法、`levels` 非空且包含 `default_level`、`param` 非空（effort/custom）。
- 加密/解密 key；返回 DTO 时掩码为 `sk-****abcd`。
- 变更后通知 LLM Manager 重建客户端（进程内热更新，见 6.4）。

### 6.3 API

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/model-providers` | admin | 列表（key 脱敏） |
| POST | `/api/model-providers` | admin | 新增/从预设添加；body 可带 `refresh_models: true` |
| PUT | `/api/model-providers/:id` | admin | 更新（key 留空表示不修改） |
| DELETE | `/api/model-providers/:id` | admin | 删除 |
| POST | `/api/model-providers/probe` | admin | 用表单中的 base URL + key 拉取模型（不落库） |
| POST | `/api/model-providers/:id/refresh-models` | admin | 刷新已保存 provider 的模型列表 |
| GET | `/api/llm-models` | 登录用户 | 聚合所有 enabled provider 的 models（id 为 `slug/modelId`，含 `reasoning` 规格） |

权限判定：`c.GetString("userID")` 对应的用户名与 `config.Env.AdminUser` 比较；新增 `AdminOnly` 中间件（或 handler 内校验），复用 `auth_service.go:172` 的既有语义。

**模型刷新交互约定**：
- 表单填写 base URL + key 后，点击"获取模型列表"调 `/probe`，返回的模型以勾选列表呈现，用户可选择纳入。
- `/probe` 与 `/refresh-models` 失败时返回可读原因（401/404/超时/非 OpenAI 格式），前端提示后可手动填写模型。
- 保存时若 `refresh_models=true` 且尚未手动填模型，服务端自动拉取一次；失败不阻塞保存（记录警告）。

### 6.4 启动与热更新

- 启动：读取 `model_providers`；集合为空时，用 `OPENAI_API_KEY` / `OPENAI_BASE_URL` / `DEFAULT_LLM_MODEL` 创建一条 `preset_id=null` 的默认 provider（bootstrap），保证升级后现有部署行为不变。
- 变更：Create/Update/Delete 成功后，服务层调用 `llm.Manager.Rebuild(providers)`——先在临时 map 构建全部客户端，成功后原子替换，失败保留旧 map 并返回错误。
- 删除 provider 时：其模型立即从 `/api/llm-models` 消失；进行中的请求使用已取得的客户端完成或失败（与现网重启语义一致）。

### 6.5 模型清单聚合

`/api/llm-models` 由 `model_providers` 生成：

```json
{"id":"deepseek/deepseek-chat","provider":"deepseek","providerName":"DeepSeek",
 "displayName":"DeepSeek Chat","maxCompletionTokens":8192,"inputTokenLimit":65536,
 "default":true,
 "reasoning":{"mode":"toggle","param":"thinking.type","levels":["on","off"],"defaultLevel":"on"}}
```

`provider` 字段供前端分组；`default` 由每条 provider 配置的第一项或显式标记决定；`reasoning` 原样透出（不含密钥）。

### 6.6 思考强度注入

`pkg/llm/openai.go` 请求构造：

```
level := requestLevel || model.default_level
switch spec.mode {
case "effort":
    if requestLevel != "" { body[spec.param] = requestLevel }
case "toggle", "custom":
    if requestLevel != "" { setPath(body, spec.param, valueFor(level)) }
case "none", "":
    // 不发送
}
```

- 只接受 `levels` 内的值，越界回退 `default_level`；
- 用户选择"默认"（空）时不注入，避免给仅支持开关的端点传 `medium`；
- 日志记录 `provider/model/effectiveLevel`（不记录 key 与请求体全文）。

### 6.7 Embedding（D3 已评估）

- 删除 `backend/pkg/embedding/gemini.go`、`GeminiDimension`、`DefaultGeminiModel`。
- `ValidateEmbeddingConfig` 简化为只支持 OpenAI-compatible；移除自动检测中的 Gemini 分支。
- `pkg/embedding/openai.go` 继续支持自定义 `EMBEDDING_BASE_URL`，生产配置无需变更。
- `GEMINI_API_KEY` 从配置读取中删除。
- 若检测到 `EMBEDDING_PROVIDER=gemini`，启动时给出明确错误，提示改为 OpenAI-compatible。

## 7. 前端设计

### 7.0 视觉一致性（强制）

新界面必须与现有 YanfaChatBI neobrutalist 风格一致，不得引入新的设计语言或组件库：

| 元素 | 现有模式（必须复用） | 位置 |
| --- | --- | --- |
| 模态容器 | `fixed inset-0 bg-black/50 backdrop-blur-sm flex items-center justify-center p-4 z-[210]` | `KnowledgeBaseModal.tsx:26` 等 |
| 面板 | 白底 + `border-2 border-black` + 硬阴影 `shadow-[4px_4px_0px_rgba(0,0,0,1)]` | 现有 modal / 模型下拉 |
| 输入框 | `.neo-input` 自定义类（`src/index.css:192`） | 所有表单 |
| 标题 | `text-sm font-bold uppercase tracking-widest` | 模型下拉 header、modal 标题 |
| 图标 | `lucide-react`（Cpu、ChevronDown、Plus、Trash2、RefreshCw 等） | 全站 |
| 按钮 | 黑底白字或白底黑边 + `border-2 border-black` + hover 位移/阴影变化 | 现有 modal 底部按钮 |
| 下拉 | `bg-white border-2 border-black shadow-[4px_4px_0px_rgba(0,0,0,1)]`，header 灰底 | `MessageInput.tsx:228-238` |

禁止事项：不引入 shadcn/Radix/新的 UI 依赖；不使用与现有 `rounded`/`border-2 border-black` 冲突的圆角与阴影；文案沿用 `zhCN` i18n 结构。

### 7.1 设置入口

- 在侧栏用户菜单（`client/src/components/chat/Sidebar.tsx` 账号区）新增"模型设置"，仅 `isAdmin` 时可见；菜单项使用现有 Sidebar 菜单项样式与图标。
- 新模态 `ModelProvidersModal`（`client/src/components/modals/`），结构复用现有 modal：
  - 容器：§7.0 的模态容器 + 白底黑边面板
  - Header：标题"模型设置"（uppercase 风格）+ 关闭按钮（X 图标）
  - Body：可滚动列表 + 添加区
  - Footer：关闭/保存按钮（黑底白字主按钮 + 白底黑边次按钮）
- 已配置列表：名称、base URL、模型数、启用状态、默认标记、操作（编辑/刷新模型/测试/停用/删除）；每行使用现有卡片的 `border-2 border-black` 与 hover 效果。
- "添加"：预设卡片（6 个）+ "自定义"；卡片样式与现有 `SelectTablesModal` 的卡片一致。
- 编辑表单：
  - 名称、base URL（`.neo-input`）
  - API key（`.neo-input`，password，占位显示已保存的掩码；留空=不修改）
  - **模型列表**：
    - "获取模型列表"按钮：用当前 base URL + key 调 `/probe`，结果以勾选列表展示（复选框行样式复用现有列表项）
    - 手动添加行：id、显示名、最大 token、上下文上限
    - 每行"思考强度"配置：模式选择（无/档位/开关/自定义）、参数名、档位、默认档位（collapsible 区块，默认折叠）
    - "刷新"按钮（已保存 provider）：调 `/refresh-models`
    - 显示"上次刷新时间"
  - 删除确认：复用 `ConfirmationModal`（现有组件），文案说明将影响引用这些模型的聊天。

### 7.2 聊天界面（模型 + 思考强度直接切换）

在现有模型选择器（`MessageInput.tsx:202-310`）上扩展，不新增浮层：

- **模型下拉**：保持现有结构（header"选择模型" + 按 provider 分组列表 + 计数），数据源改为新 API 形状。
- **思考强度区块**：在下拉列表底部新增一个分隔区块（`border-t-2 border-black`）：
  - 标题"思考强度"，与 header 同样的 uppercase 小标题
  - 当前模型有 `reasoning` 规格时，使用**滑块（slider）**选择档位，不使用按钮组：
    - 轨道样式：`h-2 bg-gray-200 border-2 border-black`；滑块 thumb：`w-5 h-5 bg-white border-2 border-black shadow-[2px_2px_0px_rgba(0,0,0,1)]`（自定义 `input[type=range]` 样式，与 neo 风格一致）
    - 档位数量 = `levels.length`，等距分布；`toggle` 模式为 2 档（关/开），`effort`/`custom` 模式按声明档位（如 低/中/高/极高/最高）
    - 滑块上方显示当前档位名（如"高"）；位置等于 `default_level` 时追加"（默认）"
    - 刻度：滑块下方左右两端显示首末档位名，避免用户不知道两端含义
    - 拖动即保存（防抖 300ms），不关闭下拉；保存失败时回滚到上一个位置并提示
  - 当前模型不支持时，整个区块隐藏
  - 无障碍：`role="slider"`、`aria-valuemin/max/now`、`aria-valuetext` 为档位名；键盘左右键可调
- **切换模型时的联动**：
  - 新模型支持 reasoning → 保留用户已选档位（若在 levels 内），否则滑块回到 `default_level` 位置
  - 新模型不支持 → 清空 `preferred_reasoning_level` 并隐藏区块
  - 提示：切换后模型名与档位标签（如 "GLM-5.3 · 高"）在 textarea 内联显示（`Cpu` 图标旁），与现有模型名展示一致
- **快捷入口**：模型按钮的 `title` 与下拉 header 中显示当前档位（如 "GLM-5.3 · 高"）。

### 7.3 状态与错误

- 保存失败：表单内错误，不关闭模态
- 拉取/测试：成功显示模型数，失败显示后端返回的脱敏原因，并允许手动填写
- 无可用 provider：模型下拉显示"未配置模型"，聊天入口给出管理员提示
- 档位切换失败（服务端保存错误）：回滚选中态并提示

## 8. 迁移与兼容

| 场景 | 处理 |
| --- | --- |
| 升级后首次启动 | `model_providers` 为空且 `OPENAI_API_KEY` 存在 → 创建默认 provider（`bootstrap`），行为与现状一致 |
| 现有 env 中配置了 Gemini/Claude/Ollama | 对应模型从清单消失；如需继续使用，管理员通过 OpenAI-compatible 端点重新添加 |
| 旧聊天 `preferred_llm_model` 为 `gemini-*`/`claude-*`/`ollama` 模型 | 模型不在新清单 → 服务端回退到默认 provider 的首个模型；聊天页面提示"原模型不可用，已切换"；不改写历史消息 |
| 旧聊天 `preferred_llm_model` 为裸模型名（如 `gpt-4o`） | 启动时生成迁移映射：若唯一匹配某 provider 的模型，重写为 `slug/modelId`；歧义时不改写，回退默认 |
| 旧聊天无 `preferred_reasoning_level` | 按模型 `default_level` 注入；若模型未声明 reasoning，不发送参数 |
| `DEFAULT_LLM_MODEL` | 仅在 bootstrap 时使用；之后由 provider 配置的 default 决定 |
| `GET /api/llm-models/provider/:provider` | 保留路径，`:provider` 改为 slug 校验（动态集合） |
| Embedding | 删除 Gemini 实现；生产当前使用 OpenAI-compatible（qwen3.7-text-embedding-flash），无需重建索引；部署时清理 `runtime.env` 中 gemini 残留键 |

## 9. 删除清单

后端（LLM）：
- `backend/pkg/llm/gemini.go`、`claude.go`、`ollama.go`
- `backend/pkg/llm/manager.go:29-40`（四个 case → 单一 compatible 构造）
- `backend/internal/constants/gemini.go`、`claude.go`；`supported_models.go` 中 `GeminiLLMModels`、`ClaudeLLMModels`、`OllamaLLMModels` 及拼接
- `backend/internal/constants/llms.go` 中 `Gemini`/`Claude`/`Ollama` 常量与 schema 分支
- `backend/internal/di/modules.go:331-500` 三段注册
- `backend/internal/apis/handlers/llm_models_handler.go` 中 Gemini 分支
- `backend/config/env_values.go`：`GeminiAPIKey`、`ClaudeAPIKey`、`OllamaBaseURL`
- 对应测试文件：`pkg/llm/*_test.go` 中 gemini/claude/ollama 用例、`constants` 相关断言

后端（Embedding）：
- `backend/pkg/embedding/gemini.go`
- `backend/pkg/embedding/types.go`：`DefaultGeminiModel`、`GeminiDimension`、相关注释
- `backend/internal/constants/embedding.go`：Gemini provider 常量、校验分支、自动检测分支
- `backend/config/env_values.go`：`GeminiAPIKey`（同时被 LLM 与 embedding 使用）

前端：
- `client/src/types/chat.ts` 中 provider 联合类型（若硬编码）
- `client/src/components/chat/MessageInput.tsx` 中 provider 显示顺序/图标（如有 gemini/claude/ollama 专用逻辑）
- 相关测试与 i18n 文案

配置与文档：
- `docker-compose/*.yml`、`backend/.env.example` 中 `GEMINI_API_KEY`、`CLAUDE_API_KEY`、`OLLAMA_BASE_URL`
- `SETUP.md`、`README.md` 中对应说明
- 部署时清理生产 `runtime.env` 的 gemini 残留键（运维步骤，不随代码提交）

## 10. 权限与安全

- 所有写接口仅管理员可用；非管理员请求返回 403。
- API key 仅加密存储；列表/详情返回 `sk-****<后4位>`；日志不打印 key 与完整请求头。
- base URL 允许 http（自部署场景需支持本地 Ollama/vLLM）；但对非管理员不可写，避免 SSRF 面扩大。
- `/probe` 只允许访问用户提交的 base URL，不代理任意路径；限制路径为 `{base}/models`。
- 思考强度参数只接受配置中声明的档位，防止任意字段注入；`custom` 模式的 `param` 限制为字母/数字/点/下划线。
- 删除 provider 不删除任何聊天数据；模型回退逻辑保证历史聊天可打开。
- 密钥加密复用 `SCHEMA_ENCRYPTION_KEY`；未配置该密钥时拒绝保存 provider（返回明确错误）。

## 11. 测试与验收

单元测试：
- `model_provider_service`：CRUD、唯一性、URL 校验、加密往返、脱敏、删除后清单变化、`Probe` 成功/401/超时、`RefreshModels` 合并与保留旧列表
- `llm.Manager.Rebuild`：原子替换、构建失败保留旧集合
- 模型聚合：多 provider、同名模型（复合 ID）、disabled provider 过滤、reasoning 规格透出
- 回退：旧 `preferred_llm_model`（裸名与复合 ID）不存在或歧义时的选择
- **思考强度**：effort/toggle/custom 三种模式的请求体构造；档位越界回退默认；"默认"不注入；未声明模型不发送参数；`levels` 校验
- Embedding：`ValidateEmbeddingConfig` 仅接受 openai；`EMBEDDING_PROVIDER=gemini` 返回明确错误

集成测试：
- 本地 mock OpenAI-compatible 服务器（httptest）：`/v1/chat/completions`、`/v1/models`
- 添加 provider（含 `refresh_models`）→ 模型出现在 `/api/llm-models` → 创建聊天选择该模型 → 生成请求带正确 base URL 与 key
- 选择思考强度后，mock 服务器断言收到 `reasoning_effort` / `thinking.type` / `enable_thinking` 对应值
- `/probe` 拉取模型后按勾选保存
- 测试连接失败（401、超时、非 JSON）后手动填写模型仍可保存
- 删除 provider 后聊天回退

前端测试：
- `ModelProvidersModal`：预设添加、自定义表单校验、掩码展示、获取模型列表（成功/失败）、reasoning 配置、删除确认
- `MessageInput`：分组渲染、无模型状态、思考强度滑块显隐、拖动/键盘调节保存（防抖）、保存失败回滚、切换模型时档位保留/回位/清空
- 视觉一致性：模态容器使用 `fixed inset-0 bg-black/50 backdrop-blur-sm`、面板 `border-2 border-black` + `shadow-[4px_4px_0px_rgba(0,0,0,1)]`、输入使用 `.neo-input`、标题 `uppercase tracking-widest`；不引入新的 UI 依赖

验收（隔离环境）：
1. 全新部署（无 `model_providers`、无 `OPENAI_API_KEY`）：管理员可添加 DeepSeek 预设，填 key → 自动/手动拉取模型 → 模型下拉出现 → 聊天可用
2. 升级部署（有 `OPENAI_API_KEY`）：启动后自动出现默认 provider，原有聊天可继续
3. 同一聊天切换模型：思考强度选择器按模型能力显示/清空
4. 选择"高"档后，mock 端点收到对应参数；选择"默认"时不发送
5. 删除 provider：模型消失，引用它的聊天回退并提示
6. 非管理员访问写接口：403
7. 日志与接口响应中无明文 key
8. `EMBEDDING_PROVIDER=gemini` 启动报明确错误（负向验收）

## 12. 风险与回滚

| 风险 | 缓解 |
| --- | --- |
| 删除 Gemini/Claude/Ollama 是 breaking change | 发布说明明示；管理员可通过 OpenAI-compatible 端点重新添加 |
| `/models` 端点兼容性差异（部分 provider 不提供或格式不同） | 拉取失败不阻塞保存；手动填写兜底；`/probe` 错误信息区分 401/404/超时 |
| 思考强度参数因 provider 差异被拒绝（400） | 配置层可关闭该模型的 reasoning；错误信息透传，聊天提示可切换档位/关闭 |
| 热更新失败导致模型不可用 | Rebuild 原子替换；失败保留旧客户端与旧配置 |
| 管理员误删 provider | 删除前二次确认；`updated_at` 审计；不做软删除（D4） |
| 旧 `preferred_llm_model` 复合 ID 迁移出错 | 仅唯一匹配时重写；歧义回退默认并提示；不改写历史消息 |

回滚：provider 数据在独立集合，回滚只需回到旧二进制；旧二进制不读该集合，行为回到 env 配置。删除三个 provider 实现不可逆，需在发布说明中声明。

## 13. 决策记录

| 编号 | 决策 | 结论 |
| --- | --- | --- |
| D1 | 预设清单 | **6 个**：OpenAI、DeepSeek、智谱 GLM、Moonshot Kimi、MiniMax、通义千问；排除 OpenRouter/SiliconFlow/火山方舟。建议默认模型已按 2026-11 官方文档更新（OpenAI gpt-6-astra、DeepSeek deepseek-v4-flash、智谱 glm-5.3、Kimi kimi-k3、MiniMax M3.1-Flash、通义 qwen3.8-max） |
| D2 | `GEMINI_API_KEY`/`CLAUDE_API_KEY`/`OLLAMA_BASE_URL` 兼容期 | **直接删除**，发布说明注明 |
| D3 | 是否删除 Gemini embedding | **删除**。评估：生产实际使用 OpenAI-compatible（qwen3.7-text-embedding-flash），Gemini embedding 未被使用；删除不影响现有 RAG；需清理 runtime.env 残留并给出启动错误提示 |
| D4 | provider 删除语义 | **硬删除 + 二次确认** |
| D5 | 同名模型区分 | **存 `slug/modelId` 复合 ID**；旧裸名在唯一匹配时迁移，歧义回退默认 |
| D6 | 本地端点无 key | **允许**，表单二次确认"该端点无需鉴权" |
| D7 | 思考强度的作用范围 | **模型级声明 + 聊天级选择**；默认档位来自模型配置；用户选"默认"时不发送参数 |

## 14. 实施拆分（供后续计划使用）

1. T1 数据模型 + repository + 加密（provider、模型 reasoning 规格、chat `preferred_reasoning_level`）
2. T2 provider service（CRUD/Probe/RefreshModels/热更新/校验）
3. T3 删除 gemini/claude/ollama（LLM 后端代码、常量、DI、API）
4. T4 删除 Gemini embedding + `ValidateEmbeddingConfig` 简化
5. T5 模型聚合 API + 旧 `preferred_llm_model` 迁移/回退
6. T6 思考强度请求注入（effort/toggle/custom + 档位校验）
7. T7 前端设置入口 + 模态（预设/自定义/获取模型列表/刷新/reasoning 配置）
8. T8 聊天框模型下拉内嵌思考强度滑块（沿用现有 neo 样式，含自定义 range 样式与无障碍）+ 切换模型联动 + 错误状态
9. T9 配置/文档/Compose 清理 + `.env.example` + 运维清理说明
10. T10 集成测试与隔离验收（mock 端点、升级场景、reasoning 断言、负向 embedding 配置）
