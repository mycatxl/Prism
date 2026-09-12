# PrismX 项目开发文档 v1.0

> 历史版本：本文件保留早期讨论，不再作为实施依据。新方向见 [项目总设计草案](DESIGN.md)，个人版的专项与实施计划仍在收敛。rpure 合并、整体重写前端优先、默认引入分布式组件等旧安排不再沿用；本文件中的进度勾选不代表新计划的完成状态。

## 项目概述

**PrismX** 是基于 Resin 代理池核心功能进行二次开发的新一代智能代理管理平台。项目保留 Resin 的核心代理池管理、协议处理和调度能力，同时重构前后台面板，并整合 rpure 的高级功能，打造更强大、更易用的统一代理管理解决方案。

### 核心定位

- **企业级代理池管理平台**：支持 10 万+ 节点规模的高性能调度
- **多协议统一网关**：HTTP/SOCKS5/反向代理三合一接入
- **智能会话保持**：业务账号级别的出口 IP 粘性绑定
- **全流程可观测**：完整的指标监控、请求日志和健康检查体系
- **零依赖部署**：单一二进制文件，内置 SQLite，开箱即用

### 技术架构目标

- **后端**：Go 1.25+，基于 Resin 核心代码重构
- **前端**：现代化 React/Vue3 技术栈，重新设计 UI/UX
- **数据库**：SQLite（强一致性 state.db + 弱一致性 cache.db）
- **代理核心**：sing-box adapter，支持主流协议
- **部署方式**：Docker / 二进制 / 源码编译

---

## 第一阶段：核心功能继承与重构

### 1.1 保留 Resin 核心能力

以下模块直接继承或最小修改使用：

#### 1.1.1 代理池核心（保留）
- **全局节点池管理**
  - 节点哈希去重机制
  - 订阅视图与节点引用计数
  - 平台可路由视图（64 分片高性能集合）
  - 节点动态健康状态管理

- **协议支持**
  - sing-box outbound adapter
  - 支持协议：socks, http, shadowsocks, vmess, trojan, wireguard, hysteria, vless, shadowtls, tuic, hysteria2, ssh
  - 订阅解析器：sing-box JSON / Clash YAML / URI 行 / 纯文本 IP:PORT

#### 1.1.2 路由与调度（保留）
- **P2C 智能选路**
  - 延迟加权 + 租约负载综合评分
  - 分域名延迟追踪（TD-EWMA 算法）
  - 三种分配策略：均衡/偏好低延迟/偏好闲置 IP

- **会话保持（Sticky Session）**
  - Platform + Account 租约模型
  - 出口 IP 锚定与同 IP 节点轮换
  - 租约过期清理与持久化

#### 1.1.3 健康检查（保留）
- **主动探测**
  - 出口 IP 探测（cloudflare.com/cdn-cgi/trace）
  - 延迟探测（可配置 URL，默认 gstatic.com）
  - 双优先级队列 + 固定 worker 池并发控制

- **被动探测**
  - 真实流量 TLS 握手延迟采样（100% 采样率）
  - 连接成功/失败反馈
  - 异步非阻塞上报

- **熔断机制**
  - 连续失败计数触发熔断
  - 主动探测自动恢复
  - 平台级熔断开关（passive_circuit_breaker_disabled）

#### 1.1.4 多协议接入（保留）
- **HTTP 正向代理**
  - Proxy-Authorization 认证
  - CONNECT 隧道 + 普通请求
  - Platform.Account:Token 身份格式

- **SOCKS5 正向代理**
  - RFC1928 标准实现
  - RFC1929 用户名密码认证
  - 隧道型双向转发

- **HTTP 反向代理**
  - URL 路径身份段：`/token/platform.account/protocol/host/path`
  - X-Resin-Account 请求头优先级最高
  - Account Header Rules 自动提取
  - WebSocket 自动检测与升级

#### 1.1.5 持久化系统（保留）
- **state.db（强一致）**
  - system_config, platforms, subscriptions, account_header_rules
  - 事务写入，API 成功即落盘

- **cache.db（弱一致）**
  - nodes_static, nodes_dynamic, node_latency, leases, subscription_nodes
  - 脏集合批量写回（容量阈值 + 定时刷新）

- **启动恢复流程**
  - 一致性修复 → 加载配置 → 注入节点 → 重建视图 → 恢复租约

#### 1.1.6 数据统计（保留）
- **实时指标**：吞吐、连接数、租约数（ring buffer）
- **历史指标**：流量、请求数、成功率、延迟分布、探测次数（bucket 聚合）
- **一次性快照**：节点池状态、延迟分布

#### 1.1.7 请求日志（保留）
- **结构化日志**
  - request_logs 表：元数据（时间、平台、账号、目标、节点、耗时、状态）
  - request_log_payloads 表：请求/响应头体（BLOB，可截断）
  - 分库滚动（单库大小限制）+ 自动清理

- **异步写入**
  - 内存队列 + 批量提交
  - 队列满直接丢弃（非阻塞）

#### 1.1.8 GeoIP 服务（保留）
- MaxMind mmdb 格式
- GitHub Release 自动更新（Cron 调度）
- 代理节点重试机制

---

### 1.2 Web API 重构（优化）

保留 Resin 的 RESTful API 设计，但进行以下优化：

#### 1.2.1 API 风格统一
- **认证方式**：Bearer Token（`Authorization: Bearer <admin_token>`）
- **错误格式**：统一 JSON 错误响应
```json
{
  "error": {
    "code": "INVALID_ARGUMENT",
    "message": "sticky_ttl is invalid"
  }
}
```
- **分页方式**：limit + offset（历史查询）/ cursor（日志查询）

#### 1.2.2 核心端点（继承 Resin）
| 分类 | 端点 | 说明 |
|------|------|------|
| 系统 | GET /api/v1/system/info | 系统信息 |
| 系统 | GET /api/v1/system/config | 全局配置 |
| 系统 | PATCH /api/v1/system/config | 更新配置 |
| 平台 | GET /api/v1/platforms | 平台列表 |
| 平台 | POST /api/v1/platforms | 创建平台 |
| 平台 | GET /api/v1/platforms/:id | 平台详情 |
| 平台 | PATCH /api/v1/platforms/:id | 更新平台 |
| 平台 | DELETE /api/v1/platforms/:id | 删除平台 |
| 订阅 | GET /api/v1/subscriptions | 订阅列表 |
| 订阅 | POST /api/v1/subscriptions | 创建订阅 |
| 订阅 | POST /api/v1/subscriptions/:id/actions/refresh | 手动刷新 |
| 节点 | GET /api/v1/nodes | 节点列表 |
| 节点 | GET /api/v1/nodes/:hash | 节点详情 |
| 租约 | GET /api/v1/platforms/:id/leases | 租约列表 |
| 租约 | DELETE /api/v1/platforms/:id/leases/:account | 释放租约 |
| 日志 | GET /api/v1/request-logs | 请求日志查询 |
| 日志 | GET /api/v1/request-logs/:id/payloads | 日志载荷 |
| 指标 | GET /api/v1/metrics/realtime/throughput | 实时吞吐 |
| 指标 | GET /api/v1/metrics/history/requests | 历史请求统计 |
| GeoIP | POST /api/v1/geoip/actions/update-now | 立即更新 |

---

### 1.3 前端完全重构（新设计）

放弃 Resin 原有前端，采用现代化技术栈重新设计：

#### 1.3.1 技术选型
- **框架**：React 18 + TypeScript
- **UI 库**：Ant Design 5 / shadcn/ui（待选）
- **状态管理**：Zustand / Jotai（轻量级）
- **路由**：React Router v6
- **图表**：Apache ECharts / Recharts
- **请求库**：Axios + React Query
- **构建工具**：Vite

#### 1.3.2 设计风格
采用深色主题为主，参考 `<design_sense>` 的设计原则：

- **色彩系统**
  - 深色背景阶梯：#05070C → #0A0D12 → #0F131C → #161D2B → #1E2636
  - 主题色：青蓝 #38BDF8（科技感）
  - 辅助色：翠绿 #6EE7B7（健康状态）
  - 警告色：琥珀 #E9A568
  - 危险色：珊瑚红 #FF6B6B

- **布局系统**
  - CSS Grid 页面框架
  - 卡片式组件布局
  - 流式响应式设计（clamp() 缩放）

- **组件风格**
  - 圆角：999px 按钮/pill，16px 卡片
  - 阴影：多层次深度感
  - 动画：流畅过渡（200-300ms）

#### 1.3.3 页面结构

**主布局**
```
+------------------+-------------------------+
|                  |                         |
|   侧边导航栏      |    主内容区              |
|   (Logo + 菜单)  |    (面包屑 + 页面内容)    |
|                  |                         |
|   [语言/登出]     |                         |
+------------------+-------------------------+
```

**核心页面**（继承 Resin 但重新设计 UI）
1. **总览看板（Dashboard）**
   - KPI 卡片：实时吞吐、连接数、节点健康率、活跃租约
   - 趋势图表：网络流量、请求成功率、节点延迟分布
   - 快捷操作：刷新订阅、查看异常节点

2. **平台管理（Platforms）**
   - 卡片网格展示平台
   - 创建平台对话框：名称、租约时长、过滤规则、分配策略
   - 平台详情页（Tabs）：监控、配置、租约、运维操作

3. **订阅管理（Subscriptions）**
   - 表格视图：名称、类型、节点数、健康节点数、状态、更新时间
   - 创建订阅：远程 URL / 本地内容
   - 批量操作：刷新、清理失效节点、删除

4. **节点池（Nodes）**
   - 高级过滤：平台、订阅、区域、出口 IP、熔断状态
   - 表格列：标签、出口 IP/区域、延迟、失败次数、状态
   - 节点详情抽屉：配置、健康历史、探测操作

5. **接入点（Endpoints）**
   - 默认接入点（只读）
   - 自定义接入点管理（端口、能力开关）

6. **请求日志（Request Logs）**
   - 时间范围 + 多维度过滤
   - 表格展示：时间、平台/账号、目标、HTTP 状态、耗时、节点
   - 日志详情：请求/响应头体查看

7. **系统配置（Settings）**
   - 分组表单：基础设置、健康检查、探测配置、日志配置
   - 实时预览：配置变更 Diff + JSON Patch

---

## 第二阶段：rpure 功能整合

### 2.1 rpure 核心功能分析

需要先明确 rpure 的核心功能有哪些，可能包括：
- IP 池管理与轮换策略
- 代理验证与质量评分
- 流量控制与限速
- 黑名单与白名单
- 更多协议支持（如 HTTP/2, HTTP/3）

**待补充**：需要详细了解 rpure 的功能特性后，再制定整合方案。

### 2.2 功能融合策略

#### 2.2.1 不冲突功能（直接添加）
- 示例：如果 rpure 有独特的代理验证算法，可以作为新的健康检查维度添加到 ProbeManager

#### 2.2.2 冲突功能（二选一或融合）
- 示例：如果 rpure 和 Resin 都有节点调度算法，需要评估优劣后选择或融合

#### 2.2.3 互补功能（增强型整合）
- 示例：rpure 的流量控制 + Resin 的会话保持 = 带宽管理的粘性代理

---

## 第三阶段：增强与优化

### 3.1 性能优化
- **热路径优化**
  - 无锁数据结构（xsync.Map, atomic）
  - O(1) 节点查找
  - 零拷贝数据转发

- **冷路径优化**
  - 后台任务协程池
  - 批量数据库写入
  - 正则预编译缓存

### 3.2 可观测性增强
- **分布式追踪**
  - OpenTelemetry 集成
  - 请求全链路追踪

- **告警系统**
  - 节点健康率阈值告警
  - 订阅更新失败告警
  - WebHook / 邮件通知

### 3.3 安全加固
- **认证增强**
  - JWT Token 替代简单 Bearer Token
  - Token 自动刷新机制
  - API 频率限制

- **数据安全**
  - 敏感字段加密存储（Account、Authorization Header）
  - 日志脱敏选项
  - HTTPS 强制重定向

### 3.4 高可用性
- **主备模式**
  - 状态同步（etcd / Redis）
  - 自动故障转移

- **水平扩展**
  - 节点池分片
  - 订阅更新任务分布式调度

---

## 开发计划与里程碑

### Phase 1：核心继承（2-3 周）
- [x] 项目初始化，创建参考目录
- [ ] 代理池核心代码迁移与重构
- [ ] 路由调度模块测试
- [ ] 健康检查系统验证
- [ ] 持久化系统适配

### Phase 2：API 重构（1-2 周）
- [ ] RESTful API 框架搭建
- [ ] 核心端点实现（平台/订阅/节点）
- [ ] 认证中间件
- [ ] API 文档生成（Swagger/OpenAPI）

### Phase 3：前端开发（3-4 周）
- [ ] 项目脚手架搭建
- [ ] 组件库封装
- [ ] 总览看板开发
- [ ] 平台管理页面
- [ ] 订阅管理页面
- [ ] 节点池页面
- [ ] 请求日志页面
- [ ] 系统配置页面

### Phase 4：rpure 整合（时间待定）
- [ ] rpure 功能调研
- [ ] 整合方案设计
- [ ] 功能开发与测试

### Phase 5：测试与优化（2 周）
- [ ] 单元测试覆盖
- [ ] 集成测试
- [ ] 性能压测（10 万节点场景）
- [ ] 安全审计
- [ ] 文档完善

### Phase 6：发布准备（1 周）
- [ ] Docker 镜像构建
- [ ] CI/CD 流水线
- [ ] 用户文档
- [ ] 快速开始指南
- [ ] 视频教程（可选）

---

## 技术难点与风险

### 4.1 技术难点
1. **10 万节点规模性能保证**
   - 风险：内存占用过大、调度延迟
   - 方案：分片存储、惰性加载、定期 GC

2. **多协议代理稳定性**
   - 风险：协议实现 bug、连接泄漏
   - 方案：充分测试、连接池管理、超时控制

3. **会话保持可靠性**
   - 风险：租约一致性、IP 漂移
   - 方案：强一致性写入、同 IP 节点轮换

### 4.2 依赖风险
- **sing-box 版本兼容性**：锁定稳定版本，及时跟进重要更新
- **订阅格式变化**：通用解析器 + 容错机制
- **GeoIP 数据源**：多数据源备份

---

## 项目结构

```
PrismX/
├── cmd/
│   └── prismx/              # 主程序入口
│       └── main.go
├── internal/                # 内部包（不对外暴露）
│   ├── api/                 # HTTP API 层
│   ├── config/              # 配置管理
│   ├── geoip/               # GeoIP 服务
│   ├── metrics/             # 指标统计
│   ├── model/               # 数据模型
│   ├── node/                # 节点池管理
│   ├── outbound/            # Outbound 适配器
│   ├── platform/            # 平台管理
│   ├── probe/               # 健康检查
│   ├── proxy/               # 代理协议实现
│   ├── requestlog/          # 请求日志
│   ├── routing/             # 路由调度
│   ├── service/             # 后台服务
│   ├── state/               # 持久化层
│   └── subscription/        # 订阅管理
├── web/                     # 前端项目
│   ├── src/
│   │   ├── api/             # API 客户端
│   │   ├── components/      # 通用组件
│   │   ├── layouts/         # 布局组件
│   │   ├── pages/           # 页面组件
│   │   ├── stores/          # 状态管理
│   │   ├── styles/          # 全局样式
│   │   └── utils/           # 工具函数
│   ├── package.json
│   └── vite.config.ts
├── docs/                    # 文档
│   ├── DESIGN.md            # 设计文档
│   ├── API.md               # API 文档
│   └── DEPLOYMENT.md        # 部署文档
├── references/              # 参考项目
│   └── Resin/               # Resin 源码参考
├── scripts/                 # 构建脚本
├── docker-compose.yml       # Docker 编排
├── Dockerfile
├── go.mod
├── go.sum
└── README.md
```

---

## 下一步行动

1. **立即开始**
   - [ ] 搭建项目骨架
   - [ ] 从 Resin 迁移核心代码（node pool + routing）
   - [ ] 编写第一个可运行的 MVP

2. **短期目标（1 个月内）**
   - [ ] 完成后端核心功能
   - [ ] 实现基础 API
   - [ ] 前端 Dashboard 原型

3. **中期目标（3 个月内）**
   - [ ] 完整前端界面
   - [ ] rpure 功能整合
   - [ ] 性能优化与测试

4. **长期目标（6 个月内）**
   - [ ] 生产环境部署
   - [ ] 社区运营
   - [ ] 企业版功能

---

## 参考资料

- [Resin 项目地址](https://github.com/Resinat/Resin)
- [Resin 设计文档](../references/Resin/DESIGN.md)
- [sing-box 文档](https://sing-box.sagernet.org/)
- [MaxMind GeoIP](https://dev.maxminddb.com/geoip/docs/databases)

---

**文档版本**：v1.0  
**最后更新**：2026-09-04  
**作者**：PrismX Team
