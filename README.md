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

歌词首次普通保存可以不填 `sourceUrl`，也可以填写有效的非托管外部参考链接；产品托管的 Vocaloid Wiki 精确 origin 只能通过服务端验证 preview/import 路径写入完整 page/revision/SHA1/fetch identity，不能仅粘贴 Wiki URL 绕过验证。历史上已经存在的仅 URL Wiki 草稿仍可继续编辑翻译，但其 URL、来源 identity 和日文来源结构必须保持不变。固定 page/revision/SHA1 只证明保存内容与某次抓取的修订一致，不证明转载、翻译或发布获授权；私有 `sourceNote`、`licenseNote` 与公开 `attribution` 都是 operator-authored metadata，系统不会把它们当作权利或许可证明。

歌词维护约定：SQLite/API 的 `LyricLine`、`LyricSegment`、`LyricRubySpan` 是编辑数据模型；Web 的分段、合并、ruby 同步行为统一放在 `web/src/lib/lyrics-segmentation.mjs`，行/分段/ruby 控件统一由 `web/src/components/lyrics/LyricsLineEditor.tsx` 渲染，页面容器不复制这套 JSX。来源 parser 只能增加通用规则，并同时添加固定 revision fixture 或合成的 fail-closed 安全测试，禁止按 music ID/歌名分支。Parser 更新后的覆盖重算使用 `lyrics-preflight -resume-incomplete-codes unsupported_format,missing_lyrics` 精准重跑确定性不完整项；该路径复用旧报告中固定的 page/revision/SHA1，跳过候选重搜，只重新抓取并解析同一 revision。`ambiguous_source` 不在白名单内，不能默认选择第一套歌词。latest schema 测试应从 `migrations` 列表读取，只有历史迁移版本与 checksum 保持显式固定。v19 会删除夹在有效文本中的 legacy 空 segment；若某行原来只有空 segment 但日文非空，则只把最小 position 的一项确定性修复为整行日文，其余空项删除；迁移后每个持久 segment 都有非空且按 text 精确复现 segment 的 ruby。旧 v18 writer 省略 `ruby_json` 的 insert 会在语句结束前自动规范成单个 exact-text span。

Phase 1 自动歌词发现默认关闭，首次配置可用 `LYRICS_DISCOVERY_ENABLED=true` 启用；种子写入后以认证设置 `lyrics_discovery.enabled` 为准。v13-v15 的 Phase 2 私有来源流水线现已实现：目录证据必须明确给出 role-bound credits 与唯一 full 目标，game-size 仅作为关联证据，缺失版本或 short/preview/partial/cover/medley 信号均不会自动抓取。唯一候选会在 discovery 完成事务中排入带完整版本化候选身份（page/revision/SHA1、page title、canonical `oldid` URL、排序 categories）的固定修订任务；多候选或目录歧义进入私有审核队列。独立 fetch worker 由 `LYRICS_FETCH_REVISION_ENABLED=true` / `lyrics_discovery.fetch_revision.enabled` 单独控制且同样默认关闭，精确核对完整候选身份，并在当前 title/URL/categories（含限制分类）相对审核快照漂移时先行拒绝，之后才原子保存有 2 MiB 上限的不可变 raw wikitext Artifact、版本化 analysis/association/job output 和三门审核项。两个 worker 只在 TCP bind 成功后启动，停机按 Drain → Cancel → Wait 后再关闭 SQLite；各自的 lease/job-timeout/idle/retry 环境变量只接受有界正整数毫秒。

Phase 2 的规范 admin API 固定为 `GET /api/admin/lyrics-source-reviews`、`GET /api/admin/lyrics-source-reviews/detail?reviewId=`、`PUT /api/admin/lyrics-source-reviews/decision`、`PUT /api/admin/lyrics-source-reviews/candidate-selection`、`POST /api/admin/lyrics-source-reviews/import`。它们仅接受 bearer JWT 管理员，PUT/POST 仅接受 JSON，使用 expected version CAS 与 actor-scoped idempotency；同一 actor 的幂等键在 single/candidate 决定账本与 batch parent 账本之间共享，只有同请求种类且 payload 完全一致才 replay，跨种类/不同 payload 或 batch 派生 child key 碰撞都会在写入前确定性返回 `idempotency_conflict` 并保持整批零应用。Console 的独立“歌词来源审核”页可选择固定候选，Artifact 则只需一次整体通过或拒绝。新的 `overall` 决定会在一个事务和一个版本增量内原子设置底层 `identity`/`source_use`/`parse` 三项兼容状态；旧的逐门决定历史仍可读取，仍处于 pending 的旧部分审核项也可用整体决定完成。冻结的列表响应只含 review identity/state、music/title、catalog fingerprint、reason、三项兼容状态、version/priority/timestamps；详情只增加候选固定身份，或安全的 page/revision/SHA1/categories/fetch facts、机器 policy/outcome/evidence、提取行、associations 与决策历史；single/candidate mutation 只回显 review/gate state/version/replayed，batch 成功响应严格为顶层 `{items,replayed}`，每个 item 严格为 `{reviewId,state,version}`。raw wikitext 及内部 artifact/analysis/extracted-line hashes 不会进入 API、列表、日志、公共文件、SSE 或 Git/S3 content backup；共享 restore 保留私有表并只把目录 fingerprint 已过期的 pending review 标记为 superseded。

已注册的 `POST /api/admin/lyrics-source-reviews/import` 是审核通过后的显式导入入口，请求闭集为 `{reviewId}`。它在 first-save 事务内重新核对 approved/gate 状态、policy、完整当前 catalog target 分组/fingerprint、不可变来源 identity、结构化提取 digest、ruby/segment 与 performer 投影，只创建 revision 1 的私有可编辑草稿，`zh-CN`/`en-US` 为空且不会发布。来源已声明但不在封闭游戏角色目录中的人声/外部歌手保留在来源证据中；存在 catalog fallback 时使用合法目录 ID，只有 `outside_character` 时则保存具体 `[]`，不伪造 ID，并由发布校验继续阻断直到编辑者处理。相同文档重放返回 `changed:false`；已有非相同歌词文档返回 `lyrics_already_saved`。单纯审核/候选决定仍不保存草稿，只有显式 import 成功提交后才发送普通私有内容变更通知。

本地 staging 链与生产 API 分离。`lyrics-stage` 的 report/manifest 仍固定 catalog contract v18，但只在完整必需列、`catalog-identity-v2` policy 与逐行 fingerprint 重算都一致时接受独立不可变的 runtime schema v18、v19 或 v20 SQLite snapshot；未来 schema 默认拒绝。新增 `lyrics-import-stage` 仅供离线本地 operator 使用，不会复制进生产镜像，也不会由服务端调用。命令要求 manifest/DB/backup/receipt 的绝对路径、已验证 backup SHA-256、审计 operator 和 `-confirm-local-offline`，会拒绝 `MOESEKAI_PRODUCTION`、取得同一 single-instance DB lock、固定 inode、拒绝 SQLite sidecar，并用 `O_EXCL` 创建私有 receipt。导入时再次验证 closed manifest/digest、完整当前目录 generation、fingerprint/target/association、page/revision/SHA1 URL、performer、空中英文翻译和 ruby/segment。不可变 manifest 会原样保留来源 performer legend；运行时数值投影只接受封闭的游戏角色目录 ID。已在 legend 声明但无法映射的人声/外部歌手会在存在时使用所选 catalog vocal fallback；若 catalog 只有 `outside_character`，私有草稿保存具体的空 `performerIds` 数组而不伪造会碰撞的游戏角色 ID，并继续由发布校验阻断，等待编辑者显式处理。整批一个事务，完全相同的已有草稿可幂等重放，非相同已有文档冲突，任一项失败则整批回滚。schema-v2 manifest 在每个 `source` 中保存该项实际固定修订抓取的 `fetchedAt`，导入时逐项写入 `sourceFetchedAt`；preflight `generatedAt` 只保留报告生成时间，不再代替来源抓取时间。示例：

```bash
cd server
go run ./cmd/lyrics-import-stage \
  -manifest /absolute/private/staging.json \
  -db /absolute/offline/moesekai.db \
  -backup /absolute/offline/moesekai-before-import.db \
  -backup-sha256 '<64-lowercase-hex>' \
  -receipt /absolute/private/import-receipt.json \
  -operator '<local-operator>' \
  -confirm-local-offline
```

活动剧情同步还会把每个 JP episode 的完整原始 Scenario 规范化为确定性紧凑 JSON，并把 SHA-256 存入无 legacy 父外键的 side table。`GET /api/event-story/episode-snapshot` 为已登录编辑器提供单事务、revision-safe 的 episode 翻译片段与 SekaiText 兼容 `sourceTalks`；原始 Scenario 只进入认证 API 和私有增量备份，绝不进入现有公共剧情 JSON。

## 实时性

控制台通过 SSE（`/sse`）接收：翻译编辑、同步/翻译进度、活动剧情更新。多用户编辑即时反映在其他在线用户界面。浏览器使用 fetch 流并在 `Authorization: Bearer` 请求头中发送当前共享会话 JWT；URL、日志与重连路径均不包含 JWT。流断开时客户端按有界退避自动重连，每次重连重新读取共享会话信封；会话 epoch 变化或卸载会立即中止旧流。

## 更新检测

默认轮询 `https://metadata.pjsk.moe/jp/versions/current_version.json` 的 `dataVersion`，并发竞速 GitHub Raw、Fastly、Gcore 和 jsDelivr 救援源，任一成功即取消其余请求。JP/CN masterdata 和 JP/CN 剧情资源均支持主源、备用源与 `{repo}` / `{branch}` 模板；masterdata 主源响应过慢时会提前启动备用源。默认 JP 剧情源为 `assets.unipjsk.com`（已移除持续返回 HTTP 525 的 snowyassets 默认链路）。同步会有限并发拉取分类及剧情资源，默认并发数为 4，可通过 `UPSTREAM_FETCH_CONCURRENCY` / `upstream.fetch_concurrency` 调整。watcher 会记录实际使用源、上次成功时间和连续失败次数，并按 `Retry-After` 或本地退避冷却 429。完整配置见 `.env.example`，可选维护本地 git 镜像（`UPSTREAM_USE_GIT=true`）。

## 备份 / 恢复

每日自动 + 手动，两个独立目标：
- **GitHub**：`git clone/commit/push` 到指定仓库与分支
- **S3 兼容**：tar.gz 上传（内置 SigV4 签名，支持 AWS S3 / Cloudflare R2 / MinIO）

恢复从任一目标拉取并重新导入 SQLite。

应用内 Git/S3 内容备份不是完整数据库备份：它不包含用户、密码哈希、token generation、设置、审计记录或加密配置。生产环境必须另行使用 SQLite 在线备份语义生成完整快照，执行 `PRAGMA integrity_check`，加密后传到独立的 off-host 存储，并定期做恢复演练。不能在 WAL 活跃时只复制 `moesekai.db` 主文件。

备份中的 `translations/` 以 `Generator.WriteAllContext` 生成的 legacy category/event restore projection 为基础；`materializeBackupPayload` 再从同一个 SQLite snapshot 明确写入与 `PublishedLyricsJSON` 字节完全一致的 `translations/lyrics/index.json` 和 `translations/lyrics/music_<id>.json` canonical public lyrics 归档，而不改变通用 legacy generator 的输出语义。published lyrics 的草稿、私有来源资料和 publication snapshot 另存于 `translation-content/lyrics.json`，用于恢复 SQLite 后由运行中的 `filesvc` 原子重建公开投影。该 materialization 与备份 push 都不是部署、静态站点发布或 CDN 同步；v2 locale projections 与 search indexes 仍不进入该归档。

增量备份继续使用 `translation-content` schemaVersion 1；`event-stories.json` 追加规范化 Scenario 与独立 `scenarioCount`，旧 `count` 语义不变。旧备份不含 Scenario 时会显式清空该 side table，恢复前会校验 SHA 与 event/episode/scenario 父身份并保持整笔事务原子。

生产配对镜像的手动发布、私有 Moe release reader GitHub App、Sigstore/attestation 与环境配置见 [`PAIRED_RELEASE.md`](PAIRED_RELEASE.md)。生产部署回滚（镜像 / 制品、数据库迁移兼容、密钥与验证）见 [`ROLLBACK_RUNBOOK.md`](ROLLBACK_RUNBOOK.md)。

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
  --build-context workspace='/path/to/verified/sekaitext-moe-workspace' \
  --build-arg WORKSPACE_MANIFEST_SHA256='<sha256-of-web-workspace-manifest.json>' \
  --build-arg WORKSPACE_ARCHIVE_SHA256='<sha256-of-producer-workspace-archive>' \
  --build-arg NEXT_REVISION="$(git rev-parse HEAD)" \
  --build-arg MOE_REVISION='<verified-40-character-moe-commit>' \
  --build-arg MOE_TAG='<verified-v-semver-moe-tag>' \
  --build-arg NODE_IMAGE='node:20.19.4-alpine3.22' \
  --build-arg NODE_IMAGE_DIGEST='<approved-64-hex-digest>' \
  --build-arg GO_IMAGE='golang:1.25.1-alpine3.22' \
  --build-arg GO_IMAGE_DIGEST='<approved-64-hex-digest>' \
  --build-arg RUNTIME_IMAGE='<approved-runtime-with-git>' \
  --build-arg RUNTIME_IMAGE_DIGEST='<approved-64-hex-digest>' \
  --build-arg VERSION='<release>' --build-arg VCS_REF="$(git rev-parse HEAD)" \
  -t moesekai-v2 .
docker run -p 8080:8080 -v moesekai-data:/data \
  -e JWT_SECRET=... -e MOESEKAI_MASTER_KEY=... \
  -e CONSOLE_ORIGIN='https://console.example.com' \
  -e ADMIN_USER=admin -e ADMIN_PASSWORD=... \
  moesekai-v2
```

`ADMIN_PASSWORD` 只应在受控首次启动或管理员恢复时临时注入；确认管理员和持久卷身份后应从长期容器环境移除。生产容器应使用镜像默认的 `DB_PATH=/data/moesekai.db`、`DATA_DIR=/data`（`.env.example` 也按容器路径编写）；本地 `go run` 才覆盖为 `./data/...`。

镜像只运行一个 Go 进程（默认 `:8080`）：同时提供静态控制台、`/api`、`/sse` 与 `/files`。生产部署必须保持单实例，并使用 `Recreate`（先停止旧实例，再启动新实例），不能让两个 SQLite writer 或两个进程内 editor gate 同时对外服务。仓库默认不附带 `seed-translations/`；如需首次迁移，应在部署前显式提供并验证种子，迁移或无损 round-trip 校验失败会终止容器启动。
运行阶段固定使用非 root 的 `65532:65532`；`/app`、二进制、控制台和配对 workspace 全部由 root 持有且不可写，只有 `/data` 与显式临时目录可写。挂载自定义宿主数据目录时必须预先授予该 UID/GID 写入权限。CI 所需的三个 digest 与 runtime 镜像名由同名 repository variables 提供，缺失或非 64 位小写十六进制值会使构建失败。

Dockerfile 的默认最终目标是 `paired`。它通过 BuildKit `workspace` named context 接收已验证的 SekaiText-Moe 制品，以 root 所有、不可写方式复制到 `/app/workspace`，并要求 `WORKSPACE_MANIFEST_SHA256` build arg；镜像永久设置 `MOESEKAI_PRODUCTION=true`，构建期间会执行 `moesekai-server --verify-workspace`，运行时不能因漏传该变量而降级。仅后端测试必须显式使用 `--target runtime`。

生产发布只可从受保护默认分支手动 dispatch `Release paired image`，并受 `production` environment 审批保护。输入为 `v<strict SemVer>` Moe tag 与 40 位小写 Moe commit。工作流用仅安装于私有 `SnowGlow-aww/SekaiText-Moe`、只有 `Contents: read` 的 GitHub App 先递归解析官方 Git tag（含 annotated tag）并确认 commit，再要求官方 release 为 `immutable: true`，在下载前限制五个 commit-addressed workspace 资产的 metadata 大小，并在下载后逐一核对实际字节数；不会下载或公开桌面 installer。NEXT 自有 Go preflight 校验 sidecar、精确 OIDC workflow/tag/SHA identity、恶意 archive 路径/类型/大小，再在新目录解包并调用上述 production workspace verifier。GHCR build 只先推 run-scoped staging tag；随后对 digest 强制 Cosign 签名、自定义 attestation、精确 NEXT workflow SHA 校验及完整 predicate 内容校验，全部成功后才把同一 digest 提升为含 NEXT full SHA 与 Moe tag/full SHA 的两个最终 tag，不推 `latest`。同 digest 重试可幂等通过，不同 digest 必须失败。仓库级并发、registry tag 不可变和 package writer 仅限该受保护工作流均为强制配置。私有仓库不支持 GitHub artifact attestation 时，producer Cosign bundle 仍为权威；支持时可显式启用额外 GitHub provenance。该工作流不执行部署。

`WORKSPACE_MANIFEST_SHA256` 锁定 schema v3 `web-workspace-manifest.json` 的原始字节；其中 source/editor-gate proof contract 必须都是 v2，并使用 revision-bound `<instanceId>:<revision>:<completedGeneration>` mutation proof。只要配置 `WORKSPACE_WEB_DIR`，服务就在取得 SQLite ownership 或打开数据库之前独立验证严格 schema、生产仓库/revision/dirty/production 状态、每条 direct-client 路由的 authentication/producerProof/allowedRoles、editor-gate 契约，以及闭集文件 size/SHA-256；未知/重复 JSON 字段、symlink、额外或缺失文件都会拒绝。`MOESEKAI_PRODUCTION=true` 必须同时配置目录和 hash，并要求 `sourceDirty=false` 且 `sourceProduction=true`。非生产可以同时省略两项；一旦配置仍完整验证。可用相同环境执行 `./moesekai-server --verify-workspace`，该模式不会取得数据库 ownership、打开数据库或启动后台任务。workspace 不会回退到 `WEB_DIR` 管理控制台，也不改变 `/api`、`/files`、`/translation` 与健康检查路径。

初始 public projection 在受跟踪的后台 worker 中异步生成；生成完成前 `/healthz` 可用而 `/readyz` 返回未就绪。任何仍未发布的失败 generation（包括启动后失败）都会按有界退避重试到成功或停止；最新 generation 处于 `Pending` 且有 `LastError` 时 `/readyz` 返回 `503`，成功重试发布后恢复 `200`。收到 drain 后，除精确的 `GET`/`HEAD /healthz` 和 `/readyz` 外，新的 API、搜索、静态文件、SSE 与 OPTIONS 请求全部返回 `503`；后台任务和 HTTP 请求结束后才关闭 SQLite。已进入的本地文件系统/SQLite projection 临界区无法安全强制取消，进程会等待其返回，这是保留的无界优雅停机风险。

## 配置

见 `.env.example`。`JWT_SECRET` 至少 32 字节，新增/重置密码为 12-72 字节。生产 `MOESEKAI_MASTER_KEY` 必须包含至少 32 字节随机 secret material；用 `openssl rand -base64 32` 生成一次，存入 secret manager，并在重启、恢复和回滚时保持稳定。`MOESEKAI_PRODUCTION=true` 会要求 immutable workspace 目录/hash 配对、合格 master key 和数据库中至少一个 admin；全新数据库必须用 `ADMIN_PASSWORD` 或 `TRANSLATOR_ACCOUNTS` 完成 bootstrap。只有 `editor`/`admin` 是有效角色，数据库存在其他角色会阻止启动。密钥项（LLM key、备份凭证）在 DB 中以 AES-GCM 加密存储，由 `MOESEKAI_MASTER_KEY` 派生密钥。env 变量仅在**首次启动**作为种子写入；之后管理设置页是唯一真源；设置更新拒绝未知键，`backup.daily_hour` 只接受规范十进制整数 `0` 到 `23`，整个 patch 原子提交。

## 测试

```bash
cd server && go test ./...
go test -race ./...
go vet ./...
cd ../web && npm ci && npm test && npm run typecheck && npm run lint && npm run build
cd .. && ./scripts/verify-release.sh
```

迁移工具自带无损往返校验：导入后从 DB 读回，逐条比对文本、来源、ID、活动剧情每行及其顺序。
