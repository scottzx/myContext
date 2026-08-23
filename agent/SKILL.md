---
name: mycontext
description: 查询和修改本地个人经营上下文系统（任务、项目、日程、容量、审计）。当用户问"今天/明天/这周要做什么""哪几天超载""某件事安排在哪天""某个项目现在什么状态"，或要求新建/改期/完成/暂缓任务、调整项目时使用。也用于回答"我有什么重要的事还没排期""有什么逾期了"。数据在本地 SQLite，通过 mycontext CLI 读写。
---

# mycontext 个人经营上下文

完整调用契约见同目录的 [USAGE.md](./USAGE.md)——**开始操作前先读它**。

## 快速判断

- 用户问状态 → `mycontext --format json ops status`（一次拿全，不要拼装多条命令）
- 用户要改东西 → 先 `task get` 拿 `version`，再带 `--expected-version` 写
- 不确定有哪些命令 → `mycontext catalog --format json`

## 最容易犯的三个错

1. 不带 `--expected-version` 就写 → 会被拒绝，或覆盖别人的修改
2. 解析错误文本而不是 `error.code` / 退出码 → 逻辑会随文案变化而崩
3. 超载时替用户决定砍哪几件事 → 这套系统的设计前提是用户自己判断
