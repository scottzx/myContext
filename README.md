# mycontext — 个人经营上下文系统 · 确定性内核

Go 实现的本地 CLI，对应两份设计文档：

- [个人经营上下文系统 B+](docs/个人经营上下文系统_B+_设计.md) — 领域模型
- [Go CLI 与静态前端技术架构](docs/个人经营上下文系统_GoCLI与静态前端_技术设计.md) — 技术边界

每次调用都是短生命周期进程：解析参数 → 定位 `MYCONTEXT_ROOT` → 只打开本命令需要的数据 →
执行 → 退出。没有 daemon，没有常驻端口。

## 当前实现范围

已完成技术分册的 **Phase 1（只读 CLI 骨架）**、**Phase 2（`ops.db v2` 与写入协议）**
与 **Phase 5（npm 分发）**，以及 agent 接入层。
Library capture、`context.db` 与静态前端尚未开始，见文末。

## 快速开始

```bash
make build
./build/mycontext --root /path/to/instance init
```

```bash
./build/mycontext --root /path/to/instance ops status
```

## 命令面

| 命令 | 作用 |
|---|---|
| `init` / `version` / `doctor` | 实例创建、版本报告、pass/warn/fail 体检 |
| `schema status\|plan\|migrate` | 迁移检查与应用（migrate 前自动快照） |
| `backup create\|verify` | 一致性快照与恢复校验 |
| `ops status` | 今天 / 明天 / 未来七天 / 逾期 / 待复查 / 超载 / 数据质量 |
| `task list\|get\|create\|update\|reschedule\|complete\|set-review` | 任务与计划 |
| `project list\|get\|create\|update\|link-kr\|tree` | 项目与 Area→Initiative→Project 导航 |
| `area create` / `initiative create` | 层级维护 |
| `schedule day\|week` | 日 / 周看板 |
| `capacity set` | 声明某天可用分钟 |
| `event list` | 审计轨迹 |
| `catalog` | 输出机器可读的操作目录，供 agent 发现能力 |

## 作为 agent 插件使用

主路径是 **CLI 调用**：任何具备 shell 工具的 agent runtime（Claude Code、Codex、
手机上的 Minis）都能直接用，不需要额外适配层。MCP 不进第一阶段——MCP server 无论
stdio 还是 HTTP 都是会话期常驻进程，而 iSH 恰好不能常驻。

- [`agent/USAGE.md`](agent/USAGE.md) — 给模型读的完整调用契约（运行时无关）
- [`agent/SKILL.md`](agent/SKILL.md) — Claude Code skill 挂载点，指向上面那份
- `mycontext catalog --format json` — 从活的命令树生成，不会与实际命令漂移

```bash
npm install -g @1agents/mycontext
mycontext init
mycontext --format json ops status
```

安装后 postinstall 会把 npm 的 bin 入口直接指向原生二进制，`mycontext` 只有一次 exec，
不经过 Node 启动（重命名前用 `@minis/context` 实测 190ms → 15ms，机制未变）。
若该步骤失败，会退回 Node launcher，行为相同只是启动慢一些。

## 写入协议

所有写入走同一条管线（技术分册 §11.2、§14）：

1. 校验枚举、日期、对象存在性 — 失败返回 `BAD_INPUT`，不落到驱动约束错误。
2. 幂等：相同 `request_id` + payload 重放首次结果；payload 不同返回 `IDEMPOTENCY_CONFLICT`。
3. 乐观并发：`expected_version` 不匹配返回 `VERSION_CONFLICT`，禁止 last-write-wins。
4. 单一短事务内更新状态表并写 `events`。
5. 返回变更摘要与 `projection_keys`，前端只刷新受影响区域。

`--dry-run` 计算真实结果后回滚，且不占用 `request_id`。

### 退出码

| 码 | 含义 | | 码 | 含义 |
|---:|---|---|---:|---|
| 0 | 成功 | | 6 | 数据库忙 |
| 2 | 参数错误 | | 7 | 需要确认 |
| 3 | 对象不存在 | | 8 | 完整性失败 |
| 4 | 歧义或版本冲突 | | 9 | 外部获取失败 |
| 5 | schema 不兼容 | | 10 | 需要恢复 |

Agent 应先判断退出码与 `error.code`，不要解析错误文本。

## 不会静默丢失事项

设计的核心不是 AI 排程，而是状态透明、调整成本低、事项不丢失。落到实现：

- **改期不覆盖**：旧 `task_schedules` 标记 `superseded` 并指向替代记录，新记录另起一行；
  `task_id` 上的部分唯一索引保证任一时刻只有一条 active 计划。
- **暂缓不消失**：`set-review` 取消当前计划但写入 `next_review_at`，任务进入 `v_review_due`。
- **暂停项目必须有复查时间**，否则拒绝写入。
- **改硬截止必须给 `--reason`**，并单独记 `deadline_changed` 事件。
- **超载只陈述事实**：任务数、计划分钟、可用分钟、超载分钟；系统不替用户挑要删的事。
- 默认容量会被明确标注为 `default capacity`，不冒充用户声明。

## 时区

确定性视图用 SQLite 的 `localtime` 判断"今天"。CLI 启动时按实例配置的时区
（默认 `Asia/Shanghai`）设置进程时区，因此视图里的自然日就是用户声明的自然日，
而不是宿主机恰好设置成什么。

## 数据布局

可执行程序与数据分离；升级二进制不触碰数据（技术分册 §8.1）：

```text
MYCONTEXT_ROOT/
├── system/        config.json · staging · recovery
├── data/          ops.db（context.db / health.db / finance.db 待建）
├── library/       YYYY/MM/DD/cap_... · _system/{orphaned,quarantine,trash}
├── snapshots/     一致性快照
├── exports/       离线导出
└── logs/
```

根目录解析顺序：`--root` → `MYCONTEXT_ROOT` → 向上查找 `.mycontext-root.json` → 平台默认
（iSH 为 `/var/minis/shared`）。每条结果的 `meta.root` 都会回报实际使用的实例。

## 构建

```bash
make release
```

CGo-free，任意宿主可交叉编译全部首批平台；`linux/386`（iSH）为必须目标。

## 尚未实现

按技术分册的阶段顺序，以下尚未开始：

- **Phase 3 Library Capture** — Capture Package / Item / Version / Component / Asset、
  manifest、staging→sealed 可恢复提交与崩溃恢复矩阵
- **`context.db`** — sources、inbox_items、entities、facts、evidence、domain_links
- **Phase 4 静态前端** — Snapshot / localhost / Minis Bridge 三种 adapter
- **Phase 6 迁移** — 旧 `tasks.db/nodes` 到 `ops.db v2` 的映射与逐条对账
- `mycontext ui serve|export`、`mycontext verify`
- npm 包尚未真正发布，也未在真实 iSH 上安装验证

Phase 0（真实 iSH 设备资格确认与基准）需要在真机上执行，尚未进行。
