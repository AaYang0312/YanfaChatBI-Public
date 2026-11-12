# 新增模型提供商 / 模型

YanfaChatBI 只依赖 **OpenAI 兼容协议**。因此大多数情况下，新增一个提供商或模型**不需要改代码**，在界面中配置即可。

## 1. 运行时配置（推荐）

1. 登录 → **更多 → 对话设置 → 模型提供商**
2. 点击「新增提供商」，选择预设或「自定义」
3. 填写：

| 字段 | 说明 |
| --- | --- |
| 名称 | 唯一，不区分大小写；用于生成 `slug` |
| Base URL | 必须指向 OpenAI 兼容入口，例如 `https://api.example.com/v1` |
| API Key | 保存后加密，读取时只返回掩码 |
| 模型列表 | 手动填写，或点击「刷新模型」从 `/models` 拉取 |
| 上下文长度 / 最大输出 | 用于提示词预算与压缩阈值 |
| 思考参数 | 可选；不填则按模型 ID 自动推断 |

4. 点击「测试连接」验证地址与 Key
5. 保存后在聊天框选择模型；支持思考的模型会显示强度滑块

模型 ID 以 `slug/model` 形式存储。切换提供商或修改 Base URL 后，客户端会重建，无需重启服务。

## 2. 内置预设

预设只影响界面中「一键填充」的默认值，不影响后端能力。新增预设：

**文件**：`client/src/components/modals/ModelProvidersModal.tsx`

```ts
{
  preset_id: 'example',
  name: 'Example AI',
  base_url: 'https://api.example.com/v1',
  default_model: 'example-chat-1',
  reasoning: { mode: 'effort', param: 'reasoning_effort', levels: ['low', 'medium', 'high'], default_level: 'medium' },
}
```

要求：

- `preset_id` 使用小写短横线命名，且不与现有预设重复
- `base_url` 必须包含版本路径（`/v1`、`/v4` 等）
- `default_model` 写该提供商实际存在的模型 ID
- 不确定思考参数时省略 `reasoning`

## 3. 思考参数推断

当提供商没有显式配置思考参数时，后端与前端会按模型 ID 推断：

| 位置 | 文件 |
| --- | --- |
| 后端 | `backend/internal/models/model_provider.go` → `DefaultReasoningSpec` |
| 前端 | `client/src/utils/reasoning.ts` → `inferReasoningSpec` |

支持的参数形态：

| `mode` | 注入方式 | 示例 |
| --- | --- | --- |
| `effort` | 请求体顶层字段 | `reasoning_effort: "high"` |
| `custom` | 点号路径 | `reasoning.effort: "high"` |
| `toggle` | 布尔或枚举开关 | `thinking.type: "enabled"` / `enable_thinking: true` |

新增推断规则时，**两侧必须同时修改**，并补充测试：

- 后端：`backend/internal/models/model_provider_test.go`
- 前端：`client/src/components/modals/ModelProvidersModal.test.tsx`

## 4. 自动化部署引导

如果不想在界面中初始化，可用环境变量在首次启动时创建默认提供商：

```bash
OPENAI_API_KEY=sk-...
OPENAI_BASE_URL=https://api.example.com/v1
DEFAULT_LLM_MODEL=example-chat-1
```

仅当数据库中没有任何提供商时生效；之后以界面配置为准。详见 [配置参考](CONFIGURATION.md)。

## 5. 注意事项

- 不要假设所有网关都支持 `response_format`；客户端已做降级重试，但仍应优先使用原生结构化输出。
- 模型列表刷新失败不应阻止手动填写模型。
- API Key 不得写入日志、测试快照或仓库。
- 新增提供商后至少验证：普通问答、工具调用、结构化回答、思考参数、长上下文压缩。

## 6. 验收清单

- [ ] 测试连接成功，模型列表可刷新
- [ ] 聊天框可选择模型并保存到会话
- [ ] 思考强度滑块档位与提供商实际参数一致
- [ ] 查询生成、执行、可视化、自动解读均使用所选模型
- [ ] 修改 Base URL / Key 后无需重启即可生效
- [ ] 文档与 `.env.example` 已同步
