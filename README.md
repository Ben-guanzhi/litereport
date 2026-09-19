# LiteReport —— 在线报表配置平台

仿 JeecgBoot「Online报表配置 / AUTO在线报表」的低代码报表平台。**后端 Go、前端纯 HTML，数据库支持 SQLite（内置，零配置开箱即用）/ MySQL / PostgreSQL（含人大金仓）/ Oracle / SQLServer / ClickHouse（驱动全部纯 Go）。**

## 快速启动

```bash
go build -o litereport.exe .        # 或 go run .
./litereport.exe                    # 默认 http://localhost:8085

> 默认登录账号 `admin / admin123`（config.yaml 的 auth 段可修改/关闭）；公共接口携带 `X-API-Token: demo-token-123` 或登录会话调用。
```

首次启动自动初始化元数据表、示例表（`demo`、`sys_log`）和 3 个示例报表（测试大数据 / 在线测试报表 / 统在线用户）。

| 页面 | 地址 |
|---|---|
| Online报表配置（列表/录入/编辑/功能测试/配置地址） | http://localhost:8085/ |
| AUTO在线报表（功能测试跳转目标） | http://localhost:8085/online/cgreport/{id} |
| 公共接口文档 + 在线调试台 | http://localhost:8085/api.html |
| 第三方系统集成指南（MES/ERP 等） | http://localhost:8085/integrate.html |
| 工作台 | http://localhost:8085/workspace.html |
| AI助手（对话式，写SQL/性能分析/表设计） | http://localhost:8085/ai.html |

## 平台能力（对照截图）

1. **报表配置管理**：按报表名称/编码搜索，录入、编辑、删除；列表展示报表SQL、数据源、创建时间。
2. **编辑弹窗**：报表编码/名字、动态数据源下拉、报表SQL（支持 `${param}` 参数）、**SQL解析**（自动生成字段明细）、两个页签：
   - 动态报表配置明细：字段名字/字段文本/宽度/类型/列显示/字段href/查询/查询模式/取值表达式/字典code/分组标题/**合计列**；
   - 报表参数：参数字段/参数文本/默认值。
3. **功能测试**：操作列「更多 → 功能测试」新窗口打开 AUTO在线报表页面，数据全部通过**公共查询接口** `/online/cgreport/api/getData/{code}` 获取，支持条件查询、排序、分页、CSV导出，并通过**公共保存接口** `saveData` / `deleteData` 演示数据写入。
4. **列汇总合计**：字段明细勾选「合计列」（数值类型）后，AUTO在线报表底部显示合计行——服务端对**过滤后的全量结果集**求 SUM（非当前页），随 COUNT 一起带 10s 缓存并在写入后失效；导出 CSV 末行同样带合计。
5. **列上筛选**：AUTO在线报表每个列头带筛选入口（▽），任意列可筛选（不限于预配置的查询字段），支持 等于/不等于/包含/枚举/区间/>/>=/</<= 九种模式，与查询区条件叠加生效；激活的筛选列头图标高亮。
6. **配置地址**：弹出菜单链接 `/online/cgreport/{id}`，可复制SQL/复制URL，用于挂接到系统菜单。

## 公共接口（无需登录，第三方可直接调用）

统一响应：`{success, message, code, result, timestamp}`。

| 方法 | 接口 | 说明 |
|---|---|---|
| GET | `/online/cgreport/api/getData/{code}` | 分页查询：`pageNo`、`pageSize`、`column`、`order`、查询字段、`${param}` 参数；`needSummary=true` 返回合计列汇总；列上筛选 `列=值`、`列_like`、`列_in=a,b`、`列_bw=a,b`、`列_ne/_gt/_ge/_lt/_le` |
| GET | `/online/cgreport/api/getDataNoPage/{code}` | 查询全部（上限10万） |
| GET | `/online/cgreport/api/getColumns/{code}` | 字段配置（渲染报表） |
| GET | `/online/cgreport/api/getInfo/{code或id}` | 完整配置（头+字段+参数） |
| POST/PUT | `/online/cgreport/api/saveData/{code}` | 保存：`{"data":{...}}` 单条 / `{"dataList":[...]}` 批量，可选 `"table"` 指定表；`data.id` 有值且存在则 UPDATE，否则 INSERT（id 自动生成） |
| POST/DELETE | `/online/cgreport/api/deleteData/{code}` | 删除：`{"ids":[...]}` 或 `?id=a,b` |
| POST | `/online/cgreport/api/importExcel/{code}` | 导入 Excel（multipart `file`，≤5MB）：首行表头=字段名，批量事务写回主表 |

```bash
# 查询（like 过滤 + 排序）
curl -H "X-API-Token: demo-token-123" "http://localhost:8085/online/cgreport/api/getData/demo_report?pageNo=1&pageSize=10&name=王&column=punch_time&order=desc"

# ${sex} 报表参数（不传则用默认值）
curl -H "X-API-Token: demo-token-123" "http://localhost:8085/online/cgreport/api/getData/ces_app_rep001?sex=男"

# 保存（新增，自动生成id）
curl -X POST -H "X-API-Token: demo-token-123" http://localhost:8085/online/cgreport/api/saveData/demo_report \
  -H "Content-Type: application/json" \
  -d '{"data":{"name":"新用户","sex":"男","age":25,"salary_money":888.88}}'

# 保存（更新指定id）
curl -X POST -H "X-API-Token: demo-token-123" http://localhost:8085/online/cgreport/api/saveData/demo_report \
  -H "Content-Type: application/json" \
  -d '{"data":{"id":"demo0001","name":"小王1","age":29}}'

# 删除
curl -X POST -H "X-API-Token: demo-token-123" http://localhost:8085/online/cgreport/api/deleteData/demo_report \
  -H "Content-Type: application/json" -d '{"ids":["demo0009"]}'
```

## 数据库支持

元数据与业务数据源均通过 `config.yaml` 切换，驱动全部为纯 Go（Oracle 使用 `go-ora`，无需安装客户端）：

```yaml
meta:                       # 报表配置存放在元数据库
  type: mysql               # sqlite | mysql | postgresql | oracle
  dsn: "root:123456@tcp(127.0.0.1:3306)/litereport?charset=utf8mb4&parseTime=true&loc=Local"

datasources:                # 动态数据源（报表查询/保存的目标库），local 固定指向元数据库
  - key: local
    name: 本地库
    type: sqlite
    dsn: ./data/litereport.db
  - key: mysql1
    name: 本地mysql
    type: mysql
    dsn: "root:123456@tcp(127.0.0.1:3306)/jeecg?charset=utf8mb4&parseTime=true&loc=Local"
  - key: pg1
    name: 本地postgres
    type: postgresql
    dsn: "host=127.0.0.1 port=5432 user=postgres password=123456 dbname=jeecg sslmode=disable"
  - key: ora1
    name: 本地oracle
    type: oracle
    dsn: "oracle://system:123456@127.0.0.1:1521/xe"
```

元数据表（`onl_cgreport_head` / `onl_cgreport_item` / `onl_cgreport_param`）与 jeecg 表结构对齐，首次启动自动创建。

## 架构

```
main.go                     启动：配置 → 连接 → 建表/种子 → 路由 → HTTP
internal/config             yaml 配置（默认零配置可运行）
internal/dbx                多数据库方言层：占位符重写(?→$n/:n)、分页包装、类型映射、
                            标识符校验/引用、连接池管理（按数据源懒加载）
internal/store              元数据存储（头表/字段/参数 CRUD，事务）
internal/service            报表引擎：SQL解析(LIMIT探测ColumnTypes)、${}参数绑定、
                            查询条件组装(=/like/in/between/>=...)、分页/排序、
                            公共保存(识别主表、INSERT/UPDATE)、公共删除
internal/api                HTTP 路由：公共接口 + 管理接口 + 静态页面 + CORS
web/                        纯 HTML/CSS/JS：index(配置) cgreport(AUTO在线报表) api(接口文档)
```

**查询流水线**：`CleanSQL 校验 → ${param} 绑定 → 包装为子查询 litrpt_data → 按字段配置拼 WHERE → 排序校验 → COUNT → 方言分页包装 → 列名小写归一化`。所有动态拼接仅限校验过的标识符，值一律走绑定参数。

**配置明细为空时**自动按 SQL 解析结果渲染，保证任意 SELECT 报表可直接预览。

## 性能优化

针对公共查询热点路径（`getData`）的分层优化：

1. **COUNT 与数据查询分离**：COUNT 查询剥离 ORDER BY 排序开销；带 **10s TTL 缓存**（翻页/轮询不重复全量计数），保存/删除数据时主动失效保证一致性。
2. **跳过 COUNT**：`needCount=false` 直接跳过计数（total/pages 返回 -1），导出与大数据量报表用 `getDataNoPage`（内部已默认跳过）。
3. **报表配置缓存**：`loadReport` 的元库 3 次查询（头/字段/参数）带 5 分钟缓存，配置保存/删除时主动失效，公共接口命中缓存时零元库开销。
4. **字段解析缓存**：未配置明细的报表自动解析结果缓存 60s，不重复执行探测查询。
5. **HTTP gzip**：JSON/HTML/CSS/JS 响应按 Accept-Encoding 自动压缩，大幅降低大数据量报表的传输体积。
6. **连接池调优**：池参数可配置（`dbpool:`）；MySQL 默认开启 `interpolateParams`（值全部绑定，客户端插值省一次服务端 prepare 往返）；SQLite 启用 WAL + busy_timeout。
7. **索引**：元库子表（item/param）与示例表常用过滤列（name/sex/create_time/log_type/user_name）启动时自动补索引。

## 安全设计

**鉴权（auth）**：管理端需登录（默认 `admin / admin123`，会话为 HttpOnly Cookie + JWT HS256，可配 `session_hours`；密码支持 bcrypt 哈希）。公共接口两种通行方式：登录会话 或 API Token（请求头 `X-API-Token` 或 `?token=`，配置于 `auth.api_tokens`）。页面未登录访问 302 跳转登录页，API 返回 401。

**查询超时（query）**：`timeout_seconds`（默认30s）贯穿全部 SQL 执行，慢 SQL 自动中断并返回友好提示，不会拖垮连接池。

**限流（ratelimit）**：令牌桶按客户端 IP——公共接口默认 20次/秒（突发40）、管理端 200次/秒、登录 10次/分钟（防爆破），超限返回 429。开发调试可设 `auth.enabled: false` 关闭鉴权。

### SQL 注入防护

平台允许配置任意 SELECT 报表 SQL（这是核心能力），因此对**所有外部输入**做了分层隔离，全部请求值不进入 SQL 文本：

1. **值一律绑定参数**：查询条件值、`${param}` 参数值、保存的数据、删除的 id，全部通过 `?` 占位符绑定，`like` 的 `%..%` 包裹在参数值上完成。
2. **标识符白名单**：进入 SQL 的表名/列名（查询列、排序列、保存列/表）必须通过 `^[A-Za-z_][A-Za-z0-9_]*$` 校验后经 `dbx.Quote` 引用（转义内嵌引号），排序列还必须匹配已配置字段。
3. **枚举白名单**：查询模式（=/like/in/between/...）、排序方向（asc/desc）均为 switch 白名单，任意其他值被忽略。
4. **报表 SQL 只读**：仅允许 SELECT/WITH、禁止分号（多语句）、禁止手写 `?`（占位符统一由 `${参数名}` 机制生成，防止与驱动占位符编号错位）；分页数字为解析后的 int。
5. **`${x}` 字面量内嵌**（如 `like '%${kw}%'`）：自动改写为方言拼接表达式（MySQL `CONCAT('%', ?, '%')`，其余 `'%' || ? || '%'`），保证占位符与参数按位对齐。
6. **`Rebind` 引号感知**：转换占位符时跳过单引号字面量内的 `?`。
7. **静态文件**：拒绝 `..` 路径穿越（net/http 防护之外的纵深防御）。

回归测试 `internal/service/report_test.go` 覆盖上述全部场景（`' OR '1'='1`、UNION 注入、排序/表名/列名/id 注入等），运行 `go test ./...` 验证。

已知边界：`getData` 能读取报表 SQL 涉及的任意数据（能力即报表本身）；公共接口需携带 API Token 或登录会话，生产环境请为数据源账号授予最小权限，并在网关层做访问控制。

**错误响应约定**：为兼容已对接的第三方系统，业务错误统一返回 HTTP 200 + body `{success:false, code, message}`；仅鉴权/限流/未登录等网关层错误返回真实 HTTP 状态码（401/403/429 等）。调用方请以 body 中的 `success` 字段为准。

## 借鉴 CloudBeaver 的第一梯队

**角色化权限**：迁移 v5 给 platform_user 加 role 列；JWT claims 带 role；中间件 requireRole(editor/admin) 路由门卫。`data-role="editor|admin"`/`class="only-admin"` 让前端按角色隐藏。admin 全权限，editor 不可见数据源/审计/推送/用户入口，viewer 进一步无写能力。

**危险 SQL 二次确认**：service.CheckDangerousSQL 检测 INTO OUTFILE/SELECT INTO/pg_sleep 等危险模式，sql/preview 命中返回 needConfirm=true 与原因列表，前端弹确认；带 confirmed=true 二次放行。

**SQL 执行历史**：迁移 v6 建 onl_sql_history 表（用户/数据源/耗时/行数/成功失败），handleSQLParse/Preview 自动记录；接口 `/api/sqlhistory?user=&pageNo=` 编辑只能看自己、admin 看全部；`/api/sqlhistory`（admin 角色）配置在工具条 `📜 SQL历史` 入口。

**单元格就地编辑 + 行详情抽屉**：AUTO在线报表单击行打开侧滑抽屉（`rowDetail` 函数），宽表场景关键字段高亮、JSON 值走大文本区域；编辑通过 `openSave(idx)` 走公共保存接口。

**导出下拉 + JSON/SQL Insert**：原 `导出CSV/导出Excel` 两按钮合并为 `导出 ⌄` 下拉，新增 `JSON 导出` (`/online/cgreport/api/exportJson/{code}`) 与 `SQL Insert 导出` (`/online/cgreport/api/exportSQLInsert/{code}`)；JSON 带 report 信息，SQL 自动识别主表并转义。

**用户管理页（`users.html`）**：导航新增 `👥用户`（仅 admin 可见），页面可新建/改角色/重置密码/删除，admin 自保不可删。

**行级数据权限（permFilter）安全提示**：`perm_filter` 是配置者编写的原始 SQL 片段（支持 `{user}` 占位），服务端拼接进 WHERE。它能限制普通用户看到的数据范围，但 **editor 角色即可编辑该片段**——片段中的子查询可读取报表数据源中该账号可查的任意表。生产环境应将报表配置权限收敛给可信人员，并为数据源账号授予最小权限。

## 优化专项（第三轮）

**生产化**：优雅停机（SIGTERM→排空→关库）、`/healthz` 就绪探针、Dockerfile 多阶段构建 + docker-compose（含MySQL演示库）、GitHub Actions CI、schema 版本迁移机制（platform_schema 表，存量库自动加列）、修改密码（导航→🔑改密，bcrypt 存储）+ 会话版本强制下线（改密后旧会话立即失效）、字典管理页面、Redis 可选限流（ratelimit.redis_addr，多实例共享窗口，未配置用内存桶）

**报表深度**：行级数据权限（报表配置 permFilter，`{user}` 占位当前用户名，仅对非 admin 生效）、渲染规则（字段配 `{"lt":0}` 标红 / `{"gt":100}` 标绿）、field_href 列链接（支持 `{value}`/`{id}` 占位）、报表参数查询区化（${param} 可在页面覆盖默认值，同名字典自动下拉）、日期快捷区间（近7天/近30天/本月/上月）、交叉表（行×列聚合，`getCrossTable` 公共接口）、分组汇总（复用图表聚合接口）、慢查询台账（>3s 自动记入审计）

**性能与工程**：Excel 大数据量流式导出（>3000行 StreamWriter 控内存）、图表数据 30s 缓存、SQL 试运行（编辑弹窗不保存直接预览前10行）、数据源表浏览器（看表结构/一键生成 select）、钉钉/企微机器人 webhook 推送渠道、前端顶栏统一注入（去重复）、Go 后端 E2E 测试（internal/api/e2e_test.go：登录→建表→写数→查询→导出→改密吊销全链路）

## 扩展功能（P1/P2）

| 功能 | 说明 | 入口/接口 |
|---|---|---|
| 数据源管理 | 页面增删改数据源，AES-GCM 加密存储DSN，保存即验证连接并热加载，无需重启 | datasources.html；`/api/datasource/*` |
| 字典翻译 | `sys_dict` 字典表，字段配置 dict_code 后视图自动值→标签翻译，查询区渲染下拉 | `GET /online/cgreport/api/getDict/{code}`（公共）；`/api/dict/*`（管理） |
| Excel 导入/导出 | 服务端生成 xlsx（表头样式/合计行/流式写出）；导入按首行表头批量事务写回主表 | `GET .../exportExcel/{code}`、`POST .../importExcel/{code}`（公共） |
| 报表分享链接 | 按报表生成免登录只读访问地址（可选有效期，随时撤销），仅开放查询/导出白名单 | 更多菜单→分享链接；`POST/DELETE /online/cgreport/head-share/{id}` |
| 审计日志 | 记录登录/报表保存删除/回滚/数据写入删除/导出/数据源变更等，含用户与IP | audit.html；`/api/audit/list` |
| 配置版本 | 每次保存自动快照，支持回滚；配置 JSON 导入导出 | 更多菜单→版本历史/导出配置；`head-versions`、`head-version/{vid}/rollback`、`head-export/{id}`、`head/import` |
| 图表与大屏 | 图表配置（柱/线/饼，维度+度量+聚合），ECharts 本地化渲染；大屏页汇总全部图表 | chart.html、bigscreen.html；`GET /online/cgreport/api/getChartData/{code}?dim=&measure=&agg=`（公共） |
| 表单开发 | 建表向导：定义字段→自动建表→自动生成报表配置，数据维护复用 AUTO在线报表 | form.html；`/online/cgform/*` |
| 定时推送 | 按分钟间隔把报表 Excel 邮件推送（SMTP 配置于 config.yaml）或推送到钉钉/企微机器人 webhook；按报表配置的数据源取数；失败可邮件告警（`smtp.alert_email`，1小时节流）；支持手动立即发送 | push.html；`/api/push/*` |
| AI 助手 | 独立对话页（SSE 流式输出、历史持久化、可附加真实表结构上下文、admin 页面化配置无需重启） | ai.html；`POST /api/ai/chat/stream`、`/api/ai/history`、`/api/ai/config` |
| 更多数据库 | 新增 SQLServer、ClickHouse 方言；人大金仓按 PostgreSQL 方式接入（type: kingbase） | dbx 方言层 |

## 已知约束

- 报表SQL仅支持单条 SELECT/WITH（不允许分号、不允许写操作）。
- `${param}` 建议整体作为值使用（`sex = '${sex}'` 或裸 `${sex}`）；嵌在字面量中间的形式不支持。
- 公共保存/删除操作报表SQL主表，需有 `id` 主键列；表名/列名做了合法性校验。
- 交叉表/分组聚合最多返回 5000 行（超出标记 `truncated:true`），高基数维度请先在 SQL 里收敛。
- 公共接口的 `?token=` 传参方式会进入浏览器历史与反向代理日志，生产环境建议统一使用 `X-API-Token` 请求头。
- 图表/交叉表聚合作用于报表 SQL 结果集，Oracle/SQLServer 下由方言层生成 TOP/ROWNUM 取数。

## 生产部署清单

上线前逐项确认：

1. **修改默认凭据**：`auth.users` 的 admin 密码（支持 bcrypt 哈希）、`auth.secret`（JWT 签名密钥）、`auth.api_tokens`（公共接口 Token，建议定期轮换）。
2. **数据源最小权限**：报表/表单所用的数据库账号只授予必要表的 SELECT（表单场景再加 INSERT/UPDATE/DELETE），禁止 DDL 与管理权限。
3. **反向代理与客户端 IP**：经 nginx 等代理部署时，在 `security.trusted_proxies` 填写代理 IP，限流与审计日志才会记录真实客户端 IP；未配置时一律取直连地址，X-Forwarded-For 不被信任。
4. **会话有效期**：`auth.session_hours` 同时控制令牌过期与 Cookie 寿命，按安全要求收紧。
5. **数据保留策略**：`onl_audit_log`（审计）、`onl_cgreport_version`（配置快照）、`onl_sql_history`（SQL 历史）目前无自动清理，建议定期归档或按周期清理（如保留 90 天），避免无限增长。
6. **备份**：SQLite 元库启动时与每日自动 `VACUUM INTO` 滚动备份到 `data/backups/`（保留 7 份）；管理端可随时 `GET /api/backup/export` 导出全量配置 JSON；MySQL/PG 场景纳入常规数据库备份。
7. **监控**：`GET /metrics`（admin 会话或 `X-API-Token` 抓取）输出 Prometheus 格式指标（请求量/耗时、连接池、查询耗时、备份时间）；`/debug/pprof/*`（admin）供性能剖析。
8. **SMTP/AI 密钥**：`smtp.password`、`ai.api_key` 属敏感信息，通过配置中心或环境变量注入，不要提交到仓库。
