# Dujiao AUV — 非官方二次开发版

基于 [Dujiao-Next](https://github.com/dujiao-next/dujiao-next) 的数字商品商城，包含 Go 后端、Vue 用户商城和管理后台。发行包将两端前端资源嵌入 `dujiao-next` 可执行文件，一个进程即可提供完整网站，无需在部署服务器安装 Go、Node.js 或 pnpm。

- **维护账号**：[homles698698-lab](https://github.com/homles698698-lab)
- **源码仓库**：[homles698698-lab/dujiao-auv](https://github.com/homles698698-lab/dujiao-auv)
- **版本下载**：[Releases](https://github.com/homles698698-lab/dujiao-auv/releases)
- **问题反馈**：[Issues](https://github.com/homles698698-lab/dujiao-auv/issues)

本项目不是上游官方发行版，不代表上游维护者或其合作伙伴。请使用本仓库的发行包，不要使用上游安装包或 Docker 镜像替换本版，否则会丢失定制功能。尚未公开正式 Release 时，下载页面可能没有可安装版本。

## 来源、修改声明与许可证

- **上游项目**：Dujiao-Next，<https://github.com/dujiao-next/dujiao-next>。
- **来源说明**：本版从名为 `dujiao-next-1.5.0` 的源码工作副本继续开发；目录名不是精确上游 tag / commit 的核验依据，当前没有可确认的原始提交记录。本版的可追溯版本以本仓库的 tag、构建提交和对应源码为准，不宣称与某个上游 tag 完全一致。
- **修改记录日期**：2026-10-26 至 2026-10-27；发行日期与版本号以对应 Release 为准。
- **许可**：本修改版作为衍生作品整体依照 **GNU GPL v3** 发布，完整条款见 [LICENSE](LICENSE)。保留上游及其他权利人的版权、许可和免责声明；新增修改不改变原代码的权利归属。不对 GPL 授予的商用、修改和再分发权利另加限制。
- **第三方组件**：独立第三方组件和素材仍遵循各自许可证及署名要求。根目录许可证不能代替第三方授权核验，也不授予上游或第三方的商标使用权。
- **无担保**：除适用法律或书面约定另有要求外，本软件按 GPL v3 所述不附带担保。请在部署前自行测试和备份。

## 本版主要修改

- 增加 MySQL 适配，保留 SQLite / PostgreSQL 支持。
- 优化数据库迁移：版本记录、已完成迁移跳过、MySQL 元数据查询与 decimal 默认值比较，以及迁移进度日志。
- 调整 Orbit 用户端主题、登录表单卡片与分组输入区，并在 PC 胶囊导航中添加登录 / 个人中心入口。
- 替换用户端和管理端默认 logo，调整认证页面的品牌展示；移除服务启动时的控制台宣传横幅。
- 更新源改为显式配置 GitHub 仓库；留空禁用远程版本检查和升级，不自动回退到上游。
- 提供干净的 [config.yml.example](config.yml.example)，完善公开源码过滤、发行包材料和 GitHub Actions 构建流程。
- 修复发行流程的 Go 依赖一致性问题及前端跨目录测试依赖安装顺序。

以上是本轮已知修改摘要，详细变更以对应版本的源码和提交记录为准。

### 验证范围

2026-10-27，维护者报告本轮 GitHub Actions 快照打包全绿，并已将对应快照部署至 VPS 完成人工功能检查。这是该次快照及该部署环境的验证记录，不代表所有平台、数据库版本、支付渠道或后续版本均已验收；正式标签构建的产物仍应在发布前核验。

## 上游合作伙伴与赞助信息（原文保留）

以下内容沿用上游 README，仅用于保留原项目背景；其中“官方”“自营”等表述均指上游项目，不表示这些机构赞助或认可本二开版。

<table>

<tr>
<td width="180"><a href="https://www.vmrack.net/?ref_code=5iXmGUMf5f5"><img src="assets/partners/vmrack.jpeg" alt="CCTK.AI" width="150"></a></td>
<td><a href="https://www.vmrack.net/?ref_code=5iXmGUMf5f5">Vmrack.com</a> 全球自动化云基础设施服务商 提供先进的云服务器、裸金属、CDN、媒体处理、对象存储和网络解决方案，助力企业轻松上云。
⚡️官方合作闪购款，仅需76刀/年，三网优化线路，助力您业务起飞，<a href="https://www.vmrack.net/vps/flash-deals/2082383856451452928?ref_code=5iXmGUMf5f5">👉点我直达</a>
</td>
</tr>

<tr>
<td width="180"><a href="https://www.99cdn.com/"><img src="assets/partners/99cdn.jpg" alt="openmodel" width="150"></a></td>
<td>99CDN 自建 CDN 平台，自主管理节点 · 智能流量调度 · 多级缓存加速。 <a href="https://www.99cdn.com/">99CDN</a> 是 EasyLink 旗下的商业化自建 CDN 与 DNS 智能调度平台，支持边缘缓存、分片缓存、多级回源、GTM 调度与边缘计算能力。</td>
</tr>

<tr>
<td width="180"><a href="https://niub.me"><img src="assets/partners/niub.png" alt="openmodel" width="150"></a></td>
<td> <a href="https://niub.me">NIUB — 数字服务，一站直达(DujiaoNext自营旗舰店)</a> 正在寻找更便捷的 AI 服务、社交账号或数字礼品卡？NIUB（niub.me）专注提供多种虚拟商品与数字服务，让不同类型的数字需求都能在一个站点完成选购。
我们重视清晰的商品信息、明确的交付方式和负责任的售后支持。每件商品的账号类型、适用地区、有效期限、使用条件与售后范围，均以对应商品页面说明为准。
访问 NIUB，探索更多数字服务与虚拟商品。</td>
</tr>



</table>

## Tech Stack

| Layer | Stack |
| --- | --- |
| Backend | Go 1.26 · Gin · GORM · SQLite / PostgreSQL / MySQL |
| Auth | JWT (separate admin / user realms) · Casbin RBAC · TOTP 2FA |
| Async | asynq on Redis (optional — the server runs without it) |
| Config | Viper (`config.yml`) |
| Frontend | Vue 3 · Vite · TypeScript · Tailwind CSS v4 · pnpm 10 |
| Admin UI | shadcn-vue / reka-ui |

## Repository Layout

```
.
├── cmd/server/               # entry point; also hosts the `admin` operator subcommands
├── internal/
│   ├── app/                  # composition root
│   │   ├── container/        # dependency-injection container
│   │   ├── httpserver/       # Gin router, route groups, middleware
│   │   └── jobs/             # asynq worker service and consumers
│   ├── bootstrap/            # per-module wiring (adapters.go + wiring.go)
│   ├── modules/              # 35 business modules — one vertical slice per domain
│   ├── workflows/            # use cases that span several modules
│   ├── platform/             # framework-facing infrastructure
│   │   ├── database/gormdb/  # connection, auto-migration
│   │   └── http/             # response envelope, Gin helpers
│   ├── shared/               # dependency-free primitives (money, jsonmap, serial …)
│   ├── authz/                # Casbin RBAC: policy model, built-in role seeds
│   ├── web/                  # SPA embedding and mounting (build-tag gated)
│   ├── architecture/         # architecture guard tests — no production code
│   ├── cache/ config/ constants/ crypto/ i18n/ logger/ queue/ version/
│   └── admincmd/ htmltext/ persistence/ telegramidentity/ testkit/ upstream/
├── frontend/
│   ├── admin/                # admin panel SPA        (dev :5174)
│   └── user/                 # customer storefront SPA (dev :5173)
├── config.yml.example           # public example; fill secrets in a private config.yml
├── Dockerfile                # single full-stack image
└── .goreleaser.yaml
```

Runtime directories created on first start: `db/` (SQLite), `uploads/`, `logs/`.

## Architecture

A modular monolith. Each domain under `internal/modules/<name>/` is a vertical slice with its
own layers:

| Layer | Holds | May import |
| --- | --- | --- |
| `domain/` | entities, value objects, business invariants | nothing from the other layers |
| `application/` | use cases, port interfaces | `domain`, `contract` |
| `infrastructure/` | GORM stores, gateways, queue adapters | `domain`, `application` ports |
| `transport/` | HTTP handlers, presenters | `application` contracts |
| `contract/` | port interfaces the application layer depends on, and the module's public surface for other modules | — |

**These rules are enforced by tests, not convention.** `internal/architecture/` parses every
import in the tree and fails the build on violations. The main ones:

- `domain` must not reach into `application`, `infrastructure`, or `transport`
- `application` must not import Gin or asynq — no transport libraries in use cases
- only a module's `infrastructure/gormstore` adapter may import GORM
- `transport` depends on application contracts, never on concrete stores
- `internal/shared` stays free of modules, GORM, Gin, and asynq
- `internal/platform` must not depend on business modules

Run them with the rest of the suite: `go test ./internal/architecture/...`

Modules never import each other's internals — they talk through `contract/`, and the wiring
lives in `internal/bootstrap/<module>/`.

### RBAC

Every `/api/v1/admin/...` route passes through Casbin. The permission catalog is generated
from the live route table, but the **built-in roles are hand-maintained** in
`internal/authz/bootstrap.go`. Adding an admin route without adding it to a role seed leaves
that route reachable only by the super admin. `internal/app/httpserver/rbac_coverage_test.go`
checks that every registered route is covered.

## Build Tags

| Tag | Effect |
| --- | --- |
| *(none)* | API only. No SPAs mounted — the default for local development. |
| `fullstack` | Embeds `internal/web/dist/{admin,user}` into the binary via `go:embed`. |
| `release` | Production behavior for outbound URL building. |

`go:embed all:dist/admin all:dist/user` requires **both** directories to exist, so a
`fullstack` build fails outright if the frontends were not built first. A plain `go build`
does not compile `embed_fullstack.go` — after touching `internal/web/`, verify with
`go build -tags release,fullstack ./cmd/server`.

## Run Modes

```bash
./dujiao-next                 # all    — HTTP server + background worker (default)
./dujiao-next -mode api       # HTTP server only
./dujiao-next -mode worker    # background worker only
```

Operator subcommands ship in the same binary, so a container needs no extra tooling:

```bash
./dujiao-next admin list-admins
./dujiao-next admin reset-password
./dujiao-next admin reset-2fa
```

## Frontend Notes

Two independent SPAs, both built with Vite and embedded at release time.

**Mount points.** The storefront is served at `/`; the admin panel at `web.admin_path`
(default `/admin`). `/api`, `/uploads`, and `/health` are reserved prefixes — an unmatched
path under them returns 404 instead of falling through to the SPA shell. Adding a new
top-level backend prefix means updating `reservedPaths` in `internal/web/handler.go`.

**The admin base path is resolved at runtime, not at build time.** Since `web.admin_path` is
configurable, `pnpm run build:fullstack` only injects a `<base href="__DJ_ADMIN_BASE__/">`
placeholder, which the server rewrites on startup. Consequences for admin code:

- native `<a href>` and `window.location` navigation must go through `adminUrl()` in
  `src/utils/adminBase.ts`
- `<router-link :to>` and `router.push()` must **not** — vue-router already carries the base,
  and prefixing again yields `/admin/admin/...`

**Storefront templates.** The customer frontend ships more than one look, selected by the
`storefront_template` site setting (`classic`, `vault`, `orbit`). Template pages live in
`src/templates/<name>/` and fall back to `src/views/` when a page has no template-specific
version; see `src/templates/registry.ts`. Append `?template=vault` to preview one locally.

**i18n.** Both frontends and all API responses are localized — Simplified Chinese, Traditional
Chinese, and English. Do not hard-code user-facing strings on either side.

## 配置说明

当前提供可公开提交的 [config.yml.example](config.yml.example)。程序实际读取 `config.yml`，不会自动读取 demo。

- 仅在首次部署、确认不存在原配置时，将 `config.yml.example` 复制为 `config.yml`；不要覆盖正在使用的配置。
- 分别运行三次 `openssl rand -hex 32`，把三个不同的随机值填入 `app.secret_key`、`jwt.secret`、`user_jwt.secret`。这些字段在示例中为空，必须先填写，不能直接用于生产。
- 已有部署不要随意更换 `app.secret_key`，否则可能无法解密已有敏感数据。管理员初始化参数及第三方凭据仅在私有运行配置中填写。
- demo 默认监听 `127.0.0.1:8080`，使用 SQLite；容器部署需按需修改监听地址，其他部署按实际情况设置数据库、可信代理、CORS 和邮件等参数。
- demo 默认关闭远程更新。不要将上游更新源直接作为本二开版的升级源，以免覆盖定制功能。

### GitHub Actions 云端打包与发布

正式发行无需本地构建。将源码上传到自己的 GitHub 仓库并启用 Actions 后：

1. 在 Actions 的 `goreleaser` 工作流选择 **Run workflow**：运行发布输入检查、安装脚本测试、后端检查与前端测试，然后构建所有配置的平台快照。下载工作流中的 snapshot artifact 检查包内容；手动运行不会创建 Release。
2. 修复检查发现的问题后，为通过验证的提交推送独立的 `v*` 版本 tag，例如 `v1.5.1`（仅为命名示例，请按自己的仓库版本序列选择，不表示已发布）。若使用 `v1.5.1-rc.1` 等预发布版本并在 GitHub 标为 prerelease，安装器的 latest release 接口不会选中它。
3. tag 触发同样的检查并生成 **草稿 Release**，包含 Windows / Linux / macOS 的 amd64 / arm64 安装包、校验和，以及指向本仓库构建提交的源码、许可证和部署说明链接。
4. 检查对应源码、许可证和包内配置示例，并在目标系统验证安装 / 启动后，再手动公开草稿。草稿不供安装器的 latest release 接口下载。

发布 token 使用 Actions 自带的 `GITHUB_TOKEN`，不需要将个人 token 或运行配置写进仓库。工作流需要仓库允许 `contents: write`；仓库或组织策略可能需要管理员授权。

配置模板统一为 `config.yml.example`。GoReleaser 归档和 Docker 最终镜像均包含 `LICENSE`；构建前使用 `go mod tidy -diff` 检查依赖而不静默改写。本版安装脚本已要求显式设置 `DUJIAO_UPDATE_REPOSITORY=owner/repository`，不会默认下载上游。运行自己的安装脚本时，在同一条 sudo 命令中通过 `env` 传入此变量，仓库名须替换成你实际信任的仓库。

当前工作流只发布二进制安装包，**不会自动推送 Docker 镜像**。Dockerfile 已补许可证及可传入的 `SOURCE_URL`、`VCS_REF`、`APP_VERSION` 元数据参数；若以后发布镜像，需要另行配置镜像构建、推送及源码说明。本地检查不等于 GitHub Actions 和目标系统安装已通过。

## 二进制部署（推荐）

从[本仓库 Releases](https://github.com/homles698698-lab/dujiao-auv/releases) 下载与你的系统和 CPU 架构匹配的归档，并核对同一 Release 提供的 SHA-256 校验文件。Linux 的 `uname -m` 输出 `x86_64` 时选择 `Linux_x86_64`，输出 `aarch64` 时选择 `Linux_arm64`。不要运行 Windows 或 macOS 的安装包。

解压后进入包含 `dujiao-next` 的目录。仅首次部署执行：

```bash
cp -n config.yml.example config.yml
chmod 600 config.yml
chmod +x dujiao-next
openssl rand -hex 32
openssl rand -hex 32
openssl rand -hex 32
```

手动编辑 `config.yml`，将三个不同随机值分别填入 `app.secret_key`、`jwt.secret`、`user_jwt.secret`；设置 `bootstrap.default_admin_username` 和符合密码策略的 `bootstrap.default_admin_password`。初始化账号配置用于首次创建管理员，不应作为已有管理员的改密方式。

模板默认使用 SQLite、监听 `127.0.0.1:8080`，可在完成配置后直接启动：

```bash
./dujiao-next
```

另开一个服务器终端检查：

```bash
curl -i http://127.0.0.1:8080/health
```

- 商城入口：`/`。
- 后台入口：默认 `/admin`，由 `web.admin_path` 控制；修改后重启，无需重新编译。
- 生产访问：使用 Nginx / aaPanel 反向代理至 `http://127.0.0.1:8080`，配置域名及 HTTPS；`cors.allowed_origins` 填实际网站来源地址。
- IP 临时内测：监听改为 `0.0.0.0`，按实际端口放行防火墙和云安全组，尽量限制访问来源。HTTP 不应用于真实敏感业务；正式部署后恢复仅本机监听。
- 使用 systemd 或面板守护进程维持服务。前台运行时关闭终端可能导致服务停止。

### aaPanel Go 项目

选择已编译的 `dujiao-next` 作为 Executable File，执行命令直接运行该文件；不要使用 `go run`、`nohup` 或末尾的 `&`。项目端口与配置保持一致。域名反向代理模式不需要开放公网后端端口。

工作目录必须是实际部署目录，以便正确读取 `config.yml` 和访问 `uploads`。运行用户（例如 `www`）必须能够读取配置、写入数据库及日志和上传目录。`chmod 600 config.yml` 后应将文件所有者设置为实际运行用户；不要使用 `777` 解决权限问题。面板的具体工作目录和守护行为以运行日志及实际设置为准。

### 数据和图片迁移

数据库保存商品和图片路径，上传文件独立保存在程序工作目录下的 `uploads/`，不会嵌入发行二进制或随远程数据库自动迁移。

迁移部署必须一起保留：

- 私有 `config.yml`，尤其是已有的 `app.secret_key`；不要重新生成后覆盖。
- 数据库：SQLite 的 `db/` 或远程 MySQL / PostgreSQL 的一致性备份。
- 完整 `uploads/` 目录及子目录结构。目标已有文件时先备份，避免盲目覆盖。

迁移后若图片 404，检查文件、工作目录及读取权限；若数据库保存的是旧站完整 URL，还需处理旧域名地址。不要让测试实例和生产实例共用数据库、同时运行后台任务来进行隔离验收。

## 版本更新与回滚

默认公开模板的 `update.repository` 为空，禁用远程更新。更新接口仍然保留，配置不会回退到上游仓库。

要启用本仓库的版本检查和二进制更新，在私有 `config.yml` 中将 `update.repository` 设为 `"homles698698-lab/dujiao-auv"`，保存并重启。值是 `owner/repository`，不是完整 URL。

启用条件与限制：

- 当前实现匿名访问 GitHub API，仓库及发行附件须可公开访问；不支持通过配置私有仓库 token 下载。
- 必须先发布非草稿、非预发布的稳定 Release。只有 tag、Actions snapshot 或 Draft Release 不构成可用更新源。
- 使用 SemVer 版本号（如 `v1.5.1`）；候选版本必须高于当前版本。非标准快照版本号可能无法比较，建议首次手动安装正式版本包。
- 保留当前 GoReleaser 平台归档命名、归档内的 `dujiao-next` 文件名及校验文件；不要只重命名附件而不调整升级器。
- 自动替换要求受支持的平台、release 构建、非容器部署，并且运行用户能写入二进制所在目录。只有二进制文件可写还不够。
- 自动重启仅在程序识别到受支持的 systemd 重启策略时启用。aaPanel 的内置守护不能直接视为已兼容，可能需要在面板手动重启；先确认更新能力提示，不要强行跳过检测。
- 更新会替换内嵌前端和后端的二进制，不会替你合并配置或迁移上传文件；新版本启动可能执行数据库迁移。升级前同时备份配置、数据库及上传目录。二进制回滚不等于数据库回滚。

已有 aaPanel 部署建议首次采用手动升级：备份数据、停止项目、替换匹配平台的二进制、保留配置和上传文件、恢复执行权限、启动并检查 `/health`、商城和后台。

### 安装脚本与 Docker

归档附带 `scripts/dujiao-next-manager.sh`，安装脚本要求显式传入 `DUJIAO_UPDATE_REPOSITORY=homles698698-lab/dujiao-auv`，只从已公开的稳定发行版安装。该脚本用于受支持的全新 Ubuntu / Debian 环境，不用于接管已有 aaPanel 部署；不要在已有部署上直接运行安装流程。

本仓库提供 Dockerfile，但当前发布工作流不自动推送 Docker 镜像。上游的 `dujiaonext/dujiao-next` 镜像不包含本版定制。自行构建镜像时应保留许可证和对应源码说明；容器更新通过新镜像重建完成，不使用后台二进制自更新。

## Quick Start (Develop)

Run the backend and the two frontends separately for hot reload. Prepare the private
`config.yml` and required secrets as described above before starting the backend:

```bash
go mod tidy && go run ./cmd/server   # :8080 — API only, no SPAs mounted

cd frontend/user  && pnpm install && pnpm run dev   # :5173
cd frontend/admin && pnpm install && pnpm run dev   # :5174
```

Both dev servers proxy `/api`, `/uploads`, `/sitemap.xml`, and `/robots.txt` to
`localhost:8080`. In production everything is same-origin, so these proxies are a
development-only concern.

> Use `pnpm` via corepack. `pnpm --dir X` does not read the `packageManager` field of the
> target directory and will pick the wrong version — `cd` into the package first.

## Building the Full-Stack Binary

> These are optional local build instructions; normal releases are built by GitHub
> Actions as described above. They do not certify a verified release. Install frontend
> dependencies from the committed lockfiles before the manual build; keep any dependency
> changes in the release commit. Run GoReleaser from a Git checkout.

```bash
goreleaser build --snapshot --single-target --clean
```

This builds both frontends, embeds them, and compiles with `-tags fullstack` — the same path
CI uses for releases. The manual equivalent:

```bash
(cd frontend/admin && pnpm run build:fullstack)   # injects the <base> placeholder
(cd frontend/user  && pnpm run build)
rm -rf internal/web/dist && mkdir -p internal/web/dist
cp -r frontend/admin/dist internal/web/dist/admin
cp -r frontend/user/dist  internal/web/dist/user
go build -tags release,fullstack -o dujiao-next ./cmd/server
```

Note that admin uses `build:fullstack`, not `build`. Plain `build` produces a bundle pinned to
`/`, which silently breaks a custom `web.admin_path`.

## Testing

```bash
go test ./...                              # full suite
go test ./internal/architecture/...        # dependency and layering guards
go test ./internal/modules/order/...       # one module

cd frontend/user  && pnpm run build        # includes vue-tsc type checking
cd frontend/admin && pnpm run build
```

Health check endpoint: `GET /health`

## Notes on Data Access

SQLite runs with `MaxOpenConns=1`. A store opens a transaction through
`WithinTransaction(func(tx contract.Transaction) error)`, and every query inside the closure
must go through that `tx` handle or a store bound to it via `WithTx(tx)`. Reaching for the
global DB handle instead asks for a second connection that will never be granted, deadlocking
the process — including indirectly, by calling a service that queries on its own. Read any
settings you need *before* opening the transaction, and keep outbound HTTP calls (payment
gateways and the like) outside it.

## 发布源码与产物的对应要求

发布本修改版的二进制、Docker 镜像或打包前端时，应按 GPL v3 提供相应的完整源码和许可材料，不能只链接到未包含本版修改的上游仓库。

- 每次发布记录独立版本号、构建所用的完整 commit SHA、发布日期和变更清单；从对应的干净提交构建，避免构建后源码与产物不一致。
- 在产物下载处明确提供对应 tag / commit 的可下载源码链接。若使用 GitHub 自动生成的源码归档，应先检查它是否完整覆盖对应源码、锁文件及必要构建脚本，而不是仅假定自动归档就足够。
- 对应源码应包含本版后端、两个前端、修改代码、必要的接口定义、依赖描述 / 锁文件以及构建和安装脚本。适用的第三方许可与署名一并保留；按 GPL 的对应源码定义处理依赖，不能用一般性的依赖链接代替必须提供的源码。
- 二进制归档和镜像应附 GPL 许可证及必要版权告知。镜像发布页说明对应源码；建议增加 `org.opencontainers.image.source`、`org.opencontainers.image.revision` 和许可证标签，方便追溯，但标签不能替代实际源码获取渠道。
- 单独分发前端构建产物时，同样应提供其对应源码及构建说明；不能只提供压缩后的 JavaScript。
- 提供源码不要求公开生产密钥、用户数据或真实数据库。可使用干净的配置示例说明如何部署。

此节为发布准备要求，不代表本版已经完成授权审计、构建验收或实际发布。许可义务以 [GPL v3 第 4–6 节](https://www.gnu.org/licenses/gpl-3.0.html) 为准。

## 上传前防泄漏检查

- 不提交运行 `config.yml`、任何 `.env*`、数据库及其备份、上传文件、日志、私钥或真实业务数据。公开仓库只保留干净配置模板。
- `.gitignore` 与 `.dockerignore` 已增加 `.env*`、运行配置副本、常见数据库文件、备份目录及私钥扩展名规则，并保留干净模板。CI / Release 使用 `scripts/check-release-inputs.py` 检查公开模板、打包材料和已跟踪的敏感路径名称；此检查不会读取 `.env*`，也不是全面的内容或历史密钥扫描。其他命名的敏感文件仍需人工核对。
- 忽略规则不会移除已跟踪文件或历史提交；直接压缩整个项目目录也不会自动应用这些规则。发布包应从确认无敏感信息的源码清单生成。
- 若历史上已提交或对外暴露密钥，应先轮换相关凭据并处理历史，不应仅删除当前文件后继续发布。

## 上游在线文档

- <https://dujiao-next.com>：上游文档，可能与本二开版存在差异；本版修改以当前仓库说明和对应版本源码为准。
