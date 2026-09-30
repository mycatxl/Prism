---
id: prism-architecture
type: architecture-design
status: draft
title: Prism runtime and console architecture
parent: prism-product
references: [prism-console-visual]
tags: [architecture, backend, frontend]
---

## 驱动

Prism 是单进程、单机优先的代理路由器与控制平面。客户端流量从统一接入层进入，经过认证、接入能力与目标策略检查，再由平台可路由视图和粘性租约选择已验证节点，最后通过 sing-box outbound 转发。订阅导入建立节点池；探测与 intel 在后台写入出口观测、证据和质量投影；平台策略据此更新候选视图。管理 API 与嵌入式 Web 控制台共享主监听器，也可以通过独立的管理监听器暴露。

## 决策

- `cmd/prism` 负责生命周期、依赖组装和监听器；领域包通过公开接口通信，代理热路径不查询数据库或外部质量服务。
- `state.db` 保存配置与权威关系，`cache.db` 保存可重建运行缓存，`intel.db` 保存检测证据与任务状态；指标和滚动请求日志使用独立存储与队列。
- sing-box 是唯一运行时内核；协议解析、节点状态、探测、质量评估、平台策略、租约路由和代理转发保持分层边界。
- 前端以 `internal/api/web/src/components/ui` 为共享控件层，以路由页面组合业务视图；API 契约由 `internal/api/server.go` 和对应 handler/service 实现。

## 不变量

- 管理 API 默认要求管理员令牌；代理令牌、平台身份、账号和租约命名空间彼此分离。
- 路由请求只使用已加载的内存配置、候选视图和运行态；配置或质量版本失效时最终检查必须拒绝不安全候选。
- 质量策略是显式配置后 fail-closed；检测服务故障不直接等同于节点故障，旧证据只能在有效期内继续使用。
- 所有输入、队列、工作池、导入、日志和持久化积压都有边界；资源耗尽按拒绝、退避或计数丢弃处理。

## 范围之外

当前代码与产品上下文不包含 SaaS、多租户、支付、跨机器状态同步、通用插件执行平台和默认 UDP ASSOCIATE。文档中的历史工作包编号只用于迁移背景，不属于运行时契约。
