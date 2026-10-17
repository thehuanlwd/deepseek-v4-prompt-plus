# DSPlus AI 开发文档

本目录存放**供 AI 模型阅读、跟踪与交接**的项目文档，会纳入版本控制。

## 阅读顺序

1. 根目录 [`CLAUDE.md`](../../CLAUDE.md) — 项目入口：架构、模块职责、常用命令
2. 根目录 [`README.md`](../../README.md) — 面向用户的功能说明与接入指南
3. 本目录下的标准、设计与坑点文档（按需查阅）

## 文档索引

| 文件 | 类型 | 说明 |
|------|------|------|
| [`YORHA_DESIGN_STANDARD.md`](YORHA_DESIGN_STANDARD.md) | 标准 | GUI 寄叶主题 UI/UX 强制规范 |
| [`LOG_DEDUPLICATION_DESIGN.md`](LOG_DEDUPLICATION_DESIGN.md) | 标准 | 分析日志自包含哈希去重设计 |
| [`高危失误记录.md`](高危失误记录.md) | 坑点 | 历史会话拼接导致内存膨胀的事故记录 |
| [`BUG_FEEDBACK_LOOP_CONTENT_EMPTY.md`](BUG_FEEDBACK_LOOP_CONTENT_EMPTY.md) | 坑点 | 推理模型 feedback 循环致 content 为空 |
| [`2026-06-07-analysis-service-design.md`](2026-06-07-analysis-service-design.md) | 设计 | 分析服务初版设计规格 |
| [`2026-06-14-analysis-lossless-storage-performance-design.md`](2026-06-14-analysis-lossless-storage-performance-design.md) | 设计 | 无损存储与大会话性能修复设计 |
| [`2026-10-07-multi-provider-model-routing-design.md`](2026-10-07-multi-provider-model-routing-design.md) | 设计 | 多供应商模型路由：冲突加前缀、默认供应商、启用开关与迁移规则 |

## 修改代码前建议

```bash
go test ./...
go test -v -run TestTransformOpenAIInPlace ./...
```

涉及分析日志逻辑时，额外阅读 `LOG_DEDUPLICATION_DESIGN.md` 与 `高危失误记录.md`。

涉及前端 UI 时，阅读 `YORHA_DESIGN_STANDARD.md`。

## 与个人笔记的区别

灵感、想法、实验记录等个人文档放在 [`其他文档/`](../../其他文档/)，该目录**不纳入版本控制**，AI 不应假设其中文件存在。