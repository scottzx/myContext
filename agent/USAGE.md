# mycontext — agent 使用说明

`mycontext` 是本地个人经营上下文系统的确定性命令行内核。你（agent）通过 shell 调用它读写
真实数据，而不是靠对话记忆推测状态。

**这份文件是给模型读的。** 它可以直接挂进 Claude Code 的 skill、Codex 的 AGENTS.md、
或 Minis 的系统提示；三者用法完全相同，因为唯一的接口就是命令行。

`mycontext ui serve` 另外起了一个本地只读网页（给人在浏览器里看仪表盘用，细节见
[README](../README.md) "本地 Web UI" 一节）。**那是给浏览器前端用的 HTTP 接口，
不是给你用的**——别去调它的 `/api/v1/invoke`。你的接口从头到尾就是下面这套 CLI 命令。

## 运行模型

每次调用都是一个短命进程：起进程 → 做一件事 → 退出。没有 daemon、没有常驻端口、
不需要 `npm run dev`。这是为了能在 iSH（iOS 上的 Alpine 环境）里跑——那里可以装 npm 包
和执行 CLI，但进程无法常驻。

```bash
mycontext --format json ops status
```

## 三条铁律

1. **先读后写。** 任何修改都要先读到对象的 `version`，再带 `--expected-version` 写回。
2. **判断退出码和 `error.code`，不要解析错误文本。** 文本会变，码不会。
3. **写操作带 `--request-id`。** 重试同一个 id 会重放首次结果，不会重复创建。

## 发现能力

不要硬编码命令表。先读目录：

```bash
mycontext catalog --format json
```

返回每个操作的规范名、参数、flag、类型、是否为写操作，以及退出码含义。

## 输出契约

`--format json` 时 stdout **只有**一个信封，诊断信息走 stderr。

成功：

```json
{"protocol":"mycontext-cli/v1","ok":true,"command":"task.reschedule",
 "data":{},"changes":[],"warnings":[],"meta":{"root":"...","schema_versions":{}}}
```

失败：

```json
{"protocol":"mycontext-cli/v1","ok":false,"command":"task.reschedule",
 "error":{"code":"VERSION_CONFLICT","message":"...","details":{},"retryable":false},"meta":{}}
```

约定：时间戳是带时区的 RFC 3339；自然日是 `YYYY-MM-DD`；ID 永远是字符串。
`changes[].projection_keys` 告诉前端哪些视图需要刷新。

## 退出码

| 码 | 含义 | 你该怎么办 |
|---:|---|---|
| 0 | 成功 | 继续 |
| 2 | 参数或输入错误 | 修正参数，别重试原样 |
| 3 | 对象不存在 | 先查再说，不要臆造 ID |
| 4 | 歧义或版本冲突 | 见下方"冲突处理" |
| 5 | schema 不兼容 | 让用户跑 `mycontext schema migrate` |
| 6 | 数据库忙 | 可重试，退避后再来 |
| 7 | 需要确认 | 缺 `--reason` 或 `--confirm`，先问用户 |
| 8 | 完整性失败 | 停下，报告用户，别再写 |
| 9 | 外部获取失败 | 可重试 |
| 10 | 未完整结束 | 需要恢复，别重复执行 |

## 冲突处理

**`VERSION_CONFLICT`（退出码 4）**：有人在你读取之后改了这个对象。
`details.current_version` 是当前值。**不要**直接拿新版本号重试——先重新读，
确认改动仍然合理，必要时问用户。

**`AMBIGUOUS_MATCH`（退出码 4）**：你用标题搜索命中多条。`details.candidates`
是候选列表。**不要**猜，把候选列给用户选。

**`IDEMPOTENCY_CONFLICT`（退出码 4）**：同一个 `--request-id` 用在了不同的载荷上。
换一个新的 request id。

## 典型流程

### 回答"今天/明天/这周什么情况"

```bash
mycontext --format json ops status
```

一次拿到：今日容量与议程、明日议程、七天负载、逾期硬截止、待复查、
重要但未排期、数据质量提示。**不需要**再调 `task list` 拼装。

回答时请区分三类信息：数据库事实、来源证据、你自己的归纳。

### 改期（先读后写）

```bash
mycontext --format json task get <id>            # 拿 version
mycontext --format json --request-id <ulid> \
  task reschedule <id> 2026-08-26 \
  --expected-version <version> --reason "本周容量不足"
```

旧计划会被标记 `superseded` 并保留，不是覆盖。

### 暂时不做，但不能丢

```bash
mycontext --format json --request-id <ulid> \
  task set-review <id> 2026-09-01 --status waiting --waiting-for "对方回复"
```

任务离开今日看板，到日期后出现在 `ops status` 的待复查里。

### 长文本、中文、Markdown 走文件

不要把长内容塞进 shell 参数，引号会毁掉它：

```bash
mycontext --format json --input payload.json task create
```

## 你不能替用户做的决定

这套系统的设计前提是**展示事实，让用户自己判断**。因此：

- **不要替用户重排优先级或挑要删的事。** 超载时如实报"计划 390 分钟 / 可用 240 分钟 /
  超载 150 分钟"，让用户自己删。
- **不要自动改期。** 改期必须是用户明确要求的。
- **不要把资料自动变成任务。** 保存文章、通知、仓库链接的默认动作是"只保存"。
- **改硬截止必须有 `--reason`**，而且应该先问用户，因为那代表外部真实约束。
- **删除、批量覆盖、对外发送必须单独确认。**

用户明确要求的单条改期或状态更新，该指令本身就是确认，不必再问一遍。

## 隐私边界

本地可以保存完整内容，但传给云端模型的应该是**最小必要**的裁剪结果。
不要把整个数据库、全量会议记录、原始健康或财务数据塞进上下文。

## 先确认实例

每条返回的 `meta.root` 是实际操作的数据实例。多实例时用 `--root` 明确指定，
不要依赖当前目录。若返回 `NOT_FOUND` 且提示没有实例，让用户先跑 `mycontext init`。
