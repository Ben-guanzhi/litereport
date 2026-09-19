# Changelog

本文件记录 LiteReport 的功能迭代与修复。格式参考 Keep a Changelog。

## [Unreleased]

### 新增

- 报表分享链接：按报表生成免登录只读访问地址（可选有效期、随时撤销），仅开放查询/导出白名单动作。
- Excel 导入：`POST /online/cgreport/api/importExcel/{code}`，首行表头按字段名匹配，批量事务写回主表；AUTO在线报表导出菜单含入口。
- 报表分类：报表头新增 `category` 字段，列表按分类筛选/展示。
- AI 助手：独立对话页（SSE 流式输出、按用户持久化历史、可附加真实表结构上下文、admin 页面化配置即时生效）；Markdown 表格（GFM 管道语法）渲染为带边框的 HTML 表格，流式增量下渐进成表。
- 可观测性：`GET /metrics`（Prometheus 格式：HTTP 请求/耗时、连接池、报表查询耗时、备份时间）；`/debug/pprof/*`（admin）；请求日志带状态码/耗时/IP 并对静态资源降噪。
- 元库备份：SQLite 每日 `VACUUM INTO` 滚动备份（保留 7 份）；`GET /api/backup/export` 全量导出；`POST /api/backup/restore` 事务化恢复（热加载数据源，不覆盖用户账号）。
- 数据保留策略：`retention:` 配置段，审计日志/SQL 历史/配置快照/AI 对话按天自动清理。
- 安全：`X-Content-Type-Options`/`X-Frame-Options`/`Referrer-Policy` 响应头；`security.trusted_proxies` 可信代理配置；登录 `next=` 站内白名单；公共接口错误脱敏；请求体 10MB 上限；CORS 收敛至公共接口且 OPTIONS 预检短路。

### 修复

- 推送：编辑任务丢失 channel/webhook；调度器改按报表数据源取数；执行结果（last_status/last_error）落库可见；优雅停机等待发送完成。
- 改密后角色丢失导致账号异常；用户管理防自删/防遮蔽 config 用户/保底 admin。
- fmtCell 仅对 ISO 日期时间替换 T（此前 "Top10" 等普通字符串被损坏，前后端一致）。
- 图表聚合在 Oracle/SQLServer 的 LIMIT 兼容（LimitWrapN）。
- AUTO在线报表导出菜单失效；危险 SQL「仍要执行」确认死循环；工作台默认页与入口。
- CI Go 版本与 go.mod 对齐；测试加 `-race`；新增 docker build 验证 job。

### 变更

- 运行时日志迁移 log/slog 结构化输出（HTTP 请求/审计/推送/备份/清理等，key=value 可机器解析）。
- index/cgreport 大页面内联脚本外置为 js/index-app.js、js/cgreport-app.js。
- Excel 导出前先查总数，超过 100000 行上限时明确提示（不再静默截断）。
- COUNT 与 SUM 合并为单次聚合扫描；Excel 导出改 `*sql.Rows` 流式写出（内存减半）并直接返回行数。
- 审计写入改缓冲队列 + 单 worker；批量保存整体事务化；缓存增加容量上限与淘汰；API Token 不再进入缓存键。
- api 层按域拆分文件；前端 LiteTable 公共表格组件迁移 7 个列表页；行操作统一 data-* 属性 + 事件委托。
- 表浏览器/数据源测试接口加 editor 角色门禁；数据源列表状态 15s 缓存（消除双倍 ping）。

## [1.0.0] — 初始版本

- Online报表配置 / AUTO在线报表 / 公共 API（查询/保存/删除/导出）。
- 多数据库：SQLite / MySQL / PostgreSQL（人大金仓）/ Oracle / SQLServer / ClickHouse。
- 表单开发、图表与大屏、字典、配置版本与回滚、审计、定时推送、行级数据权限、限流与 JWT 鉴权。
