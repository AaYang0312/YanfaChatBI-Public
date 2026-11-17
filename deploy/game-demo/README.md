# GameShelf · SDSC 6021 实体游戏网店演示版

本目录提供一套独立的 PostgreSQL 业务库和 Public 应用环境。所有店铺、订单、售价、采购成本、销量、库存均为虚构；游戏名称和实体版信息见 [SOURCES.md](SOURCES.md)。不是行情库或企业实绩。原项目 NeoBase 来源、LICENSE、NOTICE 保持不变。

独立演示构建启用 `VITE_GAME_DEMO=true`：登录页、侧栏、页面标题和分享元信息使用 GameShelf，中性图标与封面替换原产品素材；公开版原有默认构建保持原配置。登录页可打开 NeoBase 来源与许可。库名 `gameshelf_demo`、Mongo 库 `gameshelf_6021`、Compose 项目 `gameshelf-6021` 均为本演示专用，不连接原应用存储。界面知识库从全新 Mongo 开始，没有复制既有知识或向量；业务口径见本文件。本机构建可使用本地运行配置启用 BM25 + 向量检索。

## 一键启动（Windows）

完成下节的本机构建（`prepare-local.ps1` 和 `up-app -LocalBuild`）后，双击本目录的 [start-gameshelf.cmd](start-gameshelf.cmd)。脚本复用本机构建的镜像和已有数据卷，等待前后端可用后打开浏览器。更新代码后，请先重新构建。

不打开浏览器：`start-gameshelf.cmd --no-browser`。停止时使用 `./deploy/game-demo/demo.ps1 -Action stop -LocalBuild`，数据卷会保留。


## 启动与重建

在 Windows PowerShell、仓库根目录运行；先启动 Docker Desktop。演示使用 Linux 容器，因为现有 `sourcegate` 只支持 Linux，不修改它来迁就 Windows。

```powershell
# 只启动全新业务库：第一次启动自动运行 seed.sql。
./deploy/game-demo/demo.ps1 -Action up-data

# 标准源码构建，需 Docker 内能下载 Go/npm 依赖。
./deploy/game-demo/demo.ps1 -Action up-app
./deploy/game-demo/demo.ps1 -Action verify
```

如果 Docker 内依赖下载慢，使用本机已装的 Go、Node/npm 和项目依赖：

```powershell
# 新机器先在 client 执行 npm ci；backend 的 go build 会获取其依赖。
./deploy/game-demo/prepare-local.ps1
./deploy/game-demo/demo.ps1 -Action up-app -LocalBuild
./deploy/game-demo/demo.ps1 -Action verify -LocalBuild
./deploy/game-demo/smoke-api.ps1
node deploy/game-demo/ui-smoke.mjs
```

`prepare-local.ps1` 编译当前 Public 代码为 Linux amd64 后端，并将 demo 前端构建到 `.agent-runs/game-demo`，只暂存运行所需文件；不复制 `.env`、聊天或数据库卷。Go 1.25–1.26 与当前 sonic 依赖兼容；本机 Go 1.27 测试成功但会提示回退 encoding/json，属于上游兼容警告。

UI 验证使用项目已有 Playwright 和 Windows Chrome，全新临时浏览器资料，不读取个人浏览器登录；截图保存到本目录 `screenshots/`。其他系统可设置 `PLAYWRIGHT_CHROMIUM_EXECUTABLE` 指向其 Chromium。专用工作流的自然输入目前是商品检索，日期和范围来自表单；自由自然语言问题走普通问数，并需要另行配置模型。

```powershell
# 事务性重置业务数据；先停本 demo 的应用，避免读到跨重置的报表。
./deploy/game-demo/demo.ps1 -Action reset-data -LocalBuild
./deploy/game-demo/demo.ps1 -Action up-app -LocalBuild
# 只停本演示服务，数据卷保留；不会停止其他 Compose 项目。
./deploy/game-demo/demo.ps1 -Action stop -LocalBuild
./deploy/game-demo/demo.ps1 -Action status -LocalBuild
```

`seed.sql` 只接受数据库 `gameshelf_demo`，在一个事务中清理并重建指定合成表的数据；不会重置 Mongo 用户/聊天。需要一套新用户历史时使用新的独立部署，不复制旧卷。不要对其他项目使用 down/remove-volume，也不要以更换 Compose project name 代替检查端口和存储。

## 本机 Schema RAG

使用 `-LocalBuild` 时，Compose 从 Git 忽略的 `.agent-runs/game-demo/embedding.private.env` 读取 embedding 运行配置。该文件只保存在本机，提供 `EMBEDDING_PROVIDER`、`EMBEDDING_MODEL`、`EMBEDDING_BASE_URL`、`EMBEDDING_API_KEY` 四项。配置就绪后使用演示自己的 Qdrant 1.20 集合进行 BM25 + 向量检索，私有版数据库、向量索引和聊天记录不接入本演示。

现有聊天可在应用内刷新表结构以生成索引。要召回视图，先在对象选择器选择该视图，或把视图加入「全部」的范围。若要关闭 BM25，可在重建后端前设置 `$env:GAME_DEMO_SCHEMA_BM25_ENABLED='false'`。缺少 embedding 配置时，商品分析仍直接读取合成 PostgreSQL；普通自然语言生成 SQL 需要另外配置聊天模型。

## 访问与连接

- 应用：`http://localhost:55173`；后端：`http://localhost:5300`；PostgreSQL 主机端口：`127.0.0.1:55432`。
- 演示管理员：`game-demo-presenter` / `synthetic-coursework-only`。仅供本机合成演示使用。
- 在应用新建 PostgreSQL 连接：Host **game-demo-postgres**，Port **5432**，Database **gameshelf_demo**，User **game_demo_reader**，Password **synthetic-demo-reader-only**，SSL 关闭。应用容器内不要填 localhost 或 55432。
- GUI 可以先运行 `smoke-api.ps1` 自动创建独立 demo 用户的聊天并验证报告，然后登录查看该聊天。
- 普通问数优先选择 `reporting.v_game_daily`；需要指标计算原始依据时选择 `game_demo.orders/order_lines/products/shops/coverage`。不要将 `v_game_daily` 与明细订单重复相加。
- 单商品跨店分析选择「商品分析」与显式参数。demo 构建默认填入 `RE9-PS5-STD`、固定日期和六个指标。

Compose 的项目名固定为 `gameshelf-6021`，没有固定 container_name、external network/volume；Mongo、Redis、Qdrant 不对主机开放端口。所有数据卷和网络属于该项目。端口冲突时检查现有占用并另行调整 demo 配置，不要停止占用它们的其他项目。`empty.env` 防止 Compose 读取仓库默认 `.env`；Docker 上下文也排除 `.env`。

**不预装模型提供商或密钥。** 目前可直接演示「显式经营问题 → PostgreSQL 查询 → 指标计算 → 报表」。要演示普通聊天 NL→SQL 和 AI 解读，需要在这套独立应用中配置模型及 embedding 服务，选择模型并实测。专用商品报表由固定口径计算，不调用模型。

## 合成业务口径

固定主周期：`2026-11-02 ≤ business_date < 2026-11-09`，时区 `Asia/Shanghai`（与香港同 UTC+8）；前一周期 `2026-10-26..11-02`。`data_as_of = 2026-11-09T00:00:00+08:00`。演示时使用绝对日期，避免“上周”随当天变化。

销售额 = 支付成功明细数量 × 合成含义下的单位售价；成本 = 数量 × 已验证商品采购成本；毛利 = 销售额 − 成本；毛利率 = 合计毛利 / 合计销售额；平均售价 = 销售额 / 数量。货币全部 HKD，精确 decimal/numeric 计算；毛利不是扣除运费、平台费、税费和退款后的净利润。没有退款/outstock/汇率转换功能，也不把缺失事实解释为库存或营销原因。

`product_id` 表示已确定平台和版本的可售商品；`sku_id` 是单独库存标识。相同 `title` 可以对应多个商品：

| product_id | 平台 | 版本 | SKU |
| --- | --- | --- | --- |
| RE9-PS5-STD | PlayStation 5 | Standard | SKU-RE9-PS5-STD |
| NR-PS5-STD | PlayStation 5 | Standard | SKU-NR-PS5-STD |
| NR-XBOX-STD | Xbox Series X | Standard | SKU-NR-XBOX-STD |
| NR-PS5-COLLECTOR | PlayStation 5 | Collector | SKU-NR-PS5-COLLECTOR |

销售渠道为 `DEMO-WEB`，与游戏主机平台不同。虚构店铺：`CAMPUS`（Campus Quest 校园游戏店）、`PIXEL`（Pixel Union 像素玩家店）。

覆盖表独立于订单。`reporting.v_game_daily` 对 covered 无销量显示 0；对 missing 显示 NULL 并附原因；库外日期没有覆盖，不能推断 0。明细视图仅汇总覆盖完整的已知记录。

专用分析引擎目前按**整窗/店铺**判定覆盖：某店一日缺数时，该店整窗被保守排除，合计标记 partial，不能当成全网总额；趋势不会虚构该店未知数。它尚未提供精确到每天的独立覆盖合同。要展示具体缺失日，用 `v_game_daily`，或将专用报告缩到单日；不要声称当前专用周趋势能在缺一天时仍恢复其余每日全店总额。

## 推荐问题与标准答案

以下问题是演示脚本；需要模型的部分只有实测后才可声称成功。

| 场景 | 问题/参数 | 独立标准答案 |
| --- | --- | --- |
| 主案例 | 2026-11-02 至 11-08，Requiem PS5 标准版两店销售额相同，哪家毛利更高？ | CAMPUS：20 件、销售 6000、成本 3000、毛利 3000、毛利率 .5、均价 300。PIXEL：20 件、销售 6000、成本 4200、毛利 1800、毛利率 .3、均价 300。合计销售 12000、成本 7200、毛利 4800、率 .4、40 件。 |
| 事实解释 | 相同销量和销售额，差异来自哪里？ | 合成采购单位成本 150 与 210，造成每件毛利差 60；20 件毛利差 1200。不能推断真实采购议价原因或营销因果。 |
| 商品歧义 | NIGHTREIGN（只给名称） | 三个候选：PS5 Standard、Xbox Series X Standard、PS5 Collector；确认后才查询，不能合并标题。表单搜索时不要填商品 ID。 |
| 缺数 | PIXEL，NR-PS5-STD，2026-11-05 单日 | coverage=missing，sales/quantity 为 NULL；不是 0。前后两日均 2 件、400 销售额。 |
| 完整零 | NR-XBOX-STD，2026-11-08 | 两店 coverage=covered，各 0 件/0 销售额；无销售额时毛利率不可算。 |
| 正常同比窗对照 | RE9-PS5-STD 前一周与主周 | 每店前周 16 件/4800，主周 20 件/6000；数量和销售额增长 25%。不能以此证明某促销有效。 |
| 库存问数 | 2026-11-08 RE9-PS5-STD 库存 | CAMPUS 18、PIXEL 6，均合成；只支持普通 SQL/聊天问数，专用毛利工作流未实现补货预测。 |

`oracle.sql` 从原始订单明细独立计算主案例并断言，不依赖 adapter 或报告指标代码。`smoke-api.ps1` 将真实 API 报告保存在 `.agent-runs/game-demo/api-report.json`，不保存登录 token。

示例缺数趋势 SQL：

```sql
SELECT business_date, quantity, sales, coverage_status, coverage_reason
FROM reporting.v_game_daily
WHERE shop_id='PIXEL' AND product_id='NR-PS5-STD'
  AND business_date >= DATE '2026-11-02'
  AND business_date < DATE '2026-11-09'
ORDER BY business_date;
```

## 10 分钟、四人、无 Q&A

| 时间 | 负责人 | 展示动作与要点 |
| --- | --- | --- |
| 0:00–2:15 | A：对话问数与数据访问 | 说明游戏网店经营者用户与合成数据；普通问数展示具体日期、平台、版本与 SQL/结果。若模型尚未获准配置，诚实展示数据库查询并说明 NL 路径待联调，不能用录制假回答。 |
| 2:15–4:45 | B：指标与分析工作流 | 提出“两店卖得一样多，为何毛利不同”；显式商品分析查询真实 demo 库，对照 6000/3000 与 6000/1800。指出利润率不是销量排名、不是净利润。 |
| 4:45–7:15 | C：AI 解读与质量评估 | 先给独立 SQL 标准答案；区分确定性计算和 AI 解释；展示缺数 NULL、完整零、商品歧义。若启用模型，检查它是否引用正确数字、承认未知原因，并记录耗时/失败率。 |
| 7:15–10:00 | D：报告交互与用户验证 | 展示结果表/图与覆盖说明、单日缺数报告；汇报真实用户试用结果或测试方法，保留实际样本数。结束于“有证据的下一步：检查采购成本和数据导入”，不虚构商业收益。 |

四人的功能负责只是 presentation 分工，不代表每人原创整个对应模块。分别记录本组实际实现、复用和评估贡献；保留 Public/NeoBase 归属与许可。

## 评估与运行状态

按 [VERIFICATION.md](VERIFICATION.md) 区分已通过、失败、未运行。技术测试不是用户评估。让实际参与者执行同一套任务，记录是否选对 SKU、是否识别缺数、是否理解毛利/净利区别、完成时间与主观可用性；样本数和结果均需真实收集，当前不预填成功率。

模型回归建议至少包含：主案例、同名不同版、缺数、完整零、不可证实因果、越权/未知商品、禁用退款或出库口径。逐项记录数字正确性、口径、澄清行为、依据、未获支持的归因、耗时及 token/成本。先固定独立标准答案，再测模型，不以“回答更长”评分。
