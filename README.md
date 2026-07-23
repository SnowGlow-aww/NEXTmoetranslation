# Moesekai Translation v2

Project SEKAI 翻译校对系统的重构版本。前后端分离，SQLite 单一数据源，CDN 友好的文件分发，实时校对。

## 架构

```
v2/
├── server/                 Go 后端 (module moesekai/server, Go 1.25)
│   ├── main.go             组装依赖、启动 HTTP + 后台任务
│   ├── cmd/migrate/        旧 translations/ → SQLite 迁移工具（含无损校验）
│   └── internal/
│       ├── db/             SQLite 连接 + schema（modernc.org/sqlite，纯 Go）
│       ├── model/          共享类型 + 分类定义
│       ├── store/          翻译 + 活动剧情 CRUD（顺序保留）
│       ├── legacy/         旧文件加载（含 virtualLive 损坏数据恢复）
│       ├── files/          从 DB 再生成兼容格式 JSON
│       ├── filesvc/        /files/* 内存缓存服务（ETag + Cache-Control）
│       ├── searchindex/    search-index.json 生成
│       ├── config/         设置存储（AES-GCM 加密密钥）+ env 种子
│       ├── auth/           JWT + bcrypt + RBAC（admin/editor）
│       ├── translator/     CN 同步 + AI 翻译 + 活动剧情同步
│       ├── upstream/       current_version.json 轮询 + 内置 git 镜像
│       ├── backup/         S3 + GitHub 备份/恢复
│       ├── importer/       备份恢复共享导入逻辑
│       ├── sse/            Server-Sent Events 实时推送
│       └── api/            HTTP 路由 + handlers
└── web/                    Next.js 15 控制台（仿 claude.ai，无 emoji）
    └── src/
        ├── app/            页面（登录/控制台/管理设置）+ 设计系统
        ├── components/     LoginPage, Console
        └── lib/            api.ts（类型化客户端）, sse.ts, labels.ts
```

## 路径分层（CDN 友好）

| 路径 | 用途 | 缓存 |
|------|------|------|
| `/files/*` | 公开翻译数据（兼容旧格式，给 pjsk.moe） | `public, max-age, stale-while-revalidate` + ETag |
| `/api/*` | 控制台 API（JWT） | `no-store` |
| `/sse` | 实时事件推送 | 流式，不缓存 |

公开文件与旧系统**完全格式兼容**——消费端（pjsk.moe）零改动。

## 数据流

1. **编辑真源**：SQLite。所有翻译、活动剧情、来源标记、ID 追踪都存在 DB。
2. **公开分发**：DB 变更（去抖）后再生成 `/files/translation/*.json` 与 `search-index.json`。
3. **来源优先级**：pinned > human > cn > llm > unknown。

## 实时性

控制台通过 SSE（`/sse`）接收：翻译编辑、同步/翻译进度、活动剧情更新。多用户编辑即时反映在其他在线用户界面。EventSource 无法设置请求头，故 JWT 通过 `?token=` 传递。

## 更新检测

默认轮询 `https://metadata.pjsk.moe/jp/versions/current_version.json` 的 `dataVersion`，并发竞速 GitHub Raw、Fastly、Gcore 和 jsDelivr 救援源，任一成功即取消其余请求。JP/CN masterdata 和 JP/CN 剧情资源均支持主源、备用源与 `{repo}` / `{branch}` 模板；masterdata 主源响应过慢时会提前启动备用源。默认 JP 剧情源为 `assets.unipjsk.com`（已移除持续返回 HTTP 525 的 snowyassets 默认链路）。同步会有限并发拉取分类及剧情资源，默认并发数为 4，可通过 `UPSTREAM_FETCH_CONCURRENCY` / `upstream.fetch_concurrency` 调整。watcher 会记录实际使用源、上次成功时间和连续失败次数，并按 `Retry-After` 或本地退避冷却 429。完整配置见 `.env.example`，可选维护本地 git 镜像（`UPSTREAM_USE_GIT=true`）。

## 备份 / 恢复

每日自动 + 手动，两个独立目标：
- **GitHub**：`git clone/commit/push` 到指定仓库与分支
- **S3 兼容**：tar.gz 上传（内置 SigV4 签名，支持 AWS S3 / Cloudflare R2 / MinIO）

恢复从任一目标拉取并重新导入 SQLite。

生产部署回滚（镜像 / 制品、数据库迁移兼容、密钥与验证）见 [`ROLLBACK_RUNBOOK.md`](ROLLBACK_RUNBOOK.md)。

## 多用户与权限

- **admin**：管理用户、改设置、备份/恢复、触发同步，以及全部校对操作。
- **editor**：仅校对操作。

## 运行

### 本地开发

```bash
# 1. 迁移旧数据到 SQLite
cd server
go run ./cmd/migrate -src ../../translations -db ./data/moesekai.db

# 2. 启动后端
JWT_SECRET=$(openssl rand -hex 32) MOESEKAI_MASTER_KEY=dev ADMIN_USER=admin ADMIN_PASSWORD='local-admin-password' go run .

# 3. 启动前端（另开终端）
cd ../web
npm ci
npm run dev          # http://localhost:3000，自动代理 /api 到 :8080（可用 BACKEND_ORIGIN 修改）
```

### Docker

```bash
# 发布构建必须使用经审核且带 sha256 digest 的三个基础镜像。runtime 镜像需预装
# 固定版本的 git、CA 证书与 tzdata；Dockerfile 不会在构建时安装可变软件包。
docker build \
  --build-arg NODE_IMAGE='node:20.19.4-alpine3.22@sha256:<approved-digest>' \
  --build-arg GO_IMAGE='golang:1.25.1-alpine3.22@sha256:<approved-digest>' \
  --build-arg RUNTIME_IMAGE='<approved-runtime-with-git>@sha256:<approved-digest>' \
  --build-arg VERSION='<release>' --build-arg VCS_REF="$(git rev-parse HEAD)" \
  -t moesekai-v2 .
docker run -p 8080:8080 -v moesekai-data:/data \
  -e JWT_SECRET=... -e MOESEKAI_MASTER_KEY=... \
  -e ADMIN_USER=admin -e ADMIN_PASSWORD=... \
  moesekai-v2
```

镜像只运行一个 Go 进程（默认 `:8080`）：同时提供静态控制台、`/api`、`/sse` 与 `/files`。仓库默认不附带 `seed-translations/`；如需首次迁移，应在部署前显式提供并验证种子。

## 配置

见 `.env.example`。`JWT_SECRET` 至少 32 字节，新增/重置密码为 12-72 字节。密钥项（LLM key、备份凭证）在 DB 中以 AES-GCM 加密存储，由 `MOESEKAI_MASTER_KEY` 派生密钥。env 变量仅在**首次启动**作为种子写入；之后管理设置页是唯一真源。

## 测试

```bash
cd server && go test ./...
go test -race ./...
go vet ./...
cd ../web && npm ci && npm test && npm run typecheck && npm run lint && npm run build
cd .. && ./scripts/verify-release.sh
```

迁移工具自带无损往返校验：导入后从 DB 读回，逐条比对文本、来源、ID、活动剧情每行及其顺序。
