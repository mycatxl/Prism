# Prism 管理面板

正式部署优先使用根目录 `make build` 后的 `./bin/prism`（等价于 `prism run`），由 Go 进程在 `PRISM_LISTEN_ADDRESS:PRISM_PORT`（默认 `127.0.0.1:2260`）上直接提供 `/ui/` 管理入口、`/api`、HTTP 正向代理、反向代理和 SOCKS5。本目录中的 Vite / Node 服务用于独立前端开发及兼容部署。

本目录的 Node 服务默认监听 **127.0.0.1:8080**，打开 `http://127.0.0.1:8080/ui/`，并把 `/api` 反代到 `PRISM_API_TARGET`（默认 `http://127.0.0.1:2260`）。

> 注意：`PRISM_UI_HOST` / `PRISM_UI_PORT` **只影响本目录的 Node/Vite 服务**，与 Go 服务的 `PRISM_LISTEN_ADDRESS` / `PRISM_PORT` 以及可选的独立管理监听 `PRISM_ADMIN_LISTEN`（只提供 `/ui`、`/api`、`/healthz`）完全无关。运行 `bin/prism` 时请使用后者。

## 启动

要求 Node.js 22.12+。在 `web/` 中执行：

```sh
npm ci
cp .env.example .env
npm run dev
```

正式静态构建使用同一组运行配置：

```sh
npm run build
npm start
```

`npm start` 使用 Node HTTP 服务器提供 dist 和 `/api` 反代，不依赖 Vite 开发服务器。修改监听和后端地址后只需重启，无需重建前端。端口占用会失败，不自动转到其他端口。

## 配置

| 环境变量 | 默认 | 作用 |
|---|---|---|
| `PRISM_UI_HOST` | `127.0.0.1` | 管理面板监听地址 |
| `PRISM_UI_PORT` | `8080` | 管理面板端口 |
| `PRISM_API_TARGET` | `http://127.0.0.1:2260` | 后端 API origin，只在面板服务器读取 |
| `VITE_PROXY_BASE_URL` | 不设置 | 可选的公开代理地址，构建时用于地址生成；也可在平台接入页直接填写 |

通用配置的优先级为进程环境变量 > `.env.local` > `.env` > 默认值。开发模式额外读取 `.env.development` 和 `.env.development.local`；构建/preview 对应 `.env.production` 和 `.env.production.local`，模式文件优先于通用文件，进程环境变量始终最高。`npm start` 读取通用配置。

旧的 `PRISMX_UI_HOST`、`PRISMX_UI_PORT`、`PRISMX_API_TARGET` 和 `VITE_DEV_API_TARGET` 继续兼容。每个配置层先归一化旧名称，再按层级合并；同一层同时提供新旧名称时以 `PRISM_*` 为准，因此旧的进程环境变量仍可覆盖 `.env` 中的新名称。

仅 HTTP(S) origin 可作为 API 目标，不允许在 URL 中附加 token、用户名密码、路径或 query，且不能指回本机面板端口。管理面板只支持管理 API 反代，不提供 CONNECT 或 SOCKS 代理服务。

## Token 配置

当前后端已经读取环境变量及其工作目录的 `.env`：

- `PRISM_ADMIN_TOKEN`：管理登录使用的 token。
- `PRISM_PROXY_TOKEN`：HTTP/SOCKS 等代理接入使用的 token。
- `PRISM_LISTEN_ADDRESS` / `PRISM_PORT`：后端自身监听地址和端口，与面板的 8080 分开。

统一后端配置在项目根目录 `.env`，示例见 [根目录环境示例](../.env.example)。首次部署运行 `./bin/prism init` 生成两个独立随机令牌；已有配置继续保留。修改环境后重启后端即可，无需修改前端源码。

面板登录时输入管理员 token；面板反代只传递浏览器已提供的 Authorization，**不会将环境中的管理员 token 自动注入匿名请求，也不提供查询 token 的接口**。浏览器 token 仅保留当前 tab 会话。关闭 tab 后重新登录；平台接入页的代理 token 也按会话保存。

所有 `VITE_*` 都是公开的构建配置，绝不用于 token。真实 `.env`、数据目录和本地覆盖已加入忽略规则；开源仓库只提交 example。Prism 启动时拒绝缺失或为空的管理员 / 代理令牌。

## 验证与边界

```sh
npm run test:config
npm run lint
npm run build
```

`test:config` 验证默认端口、新旧环境变量覆盖、危险 target 拒绝、深链接、静态文件边界、分块请求体限制，以及凭证不会被自动注入。浏览器回归位于 scripts/，默认启动根目录 `bin/prism standalone`，也可用 `PRISM_TEST_BACKEND` 指定二进制，旧 `PRISMX_TEST_BACKEND` 继续兼容。

本次已接入的是 Resin 现有节点、订阅、平台、探测、接入点、日志和设置 API。新增信誉评分、住宅类型、同出口优先级与 rotate 服务仍属后端设计，当前前端不伪造这些结果。

监听改为局域网/公网必须显式设置 host；远程管理建议在可信 HTTPS/隧道后运行并保持后端鉴权开启。
