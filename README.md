# XMall — 非官方二次开发版

本项目基于 [Dujiao-Next](https://github.com/dujiao-next/dujiao-next) 二次开发，包含 Go 后端、用户商城和管理后台。本项目不是上游官方发行版，也不代表上游维护者或其合作伙伴。

## 来源、修改声明与许可证

- **上游项目**：Dujiao-Next，<https://github.com/dujiao-next/dujiao-next>。
- **基础版本核验状态**：当前工作副本来自名为 `dujiao-next-1.5.0` 的目录；该名称仅作来源线索，尚未通过上游 tag 或 commit 核实。正式发布前须补充准确的上游版本及提交号，不应将目录名当作已核验版本。
- **修改声明日期**：2026-10-26。本工作副本已作修改，下方列出本轮二次开发的主要内容；此日期不是所有文件的修改时间，也不是正式发布日期。
- **维护账号与发布地址**：[XManCodeing](https://github.com/XManCodeing) · [XMall 源码仓库](https://github.com/XManCodeing/XMall) · [版本下载](https://github.com/XManCodeing/XMall/releases)。仓库由 `dujiao-auv` 重命名而来，保留历史提交和版本；新品牌发行包使用 `xmall` 前缀，实际版本以已发布的 tag 为准。
- **许可**：本修改版作为衍生作品整体依照 **GNU GPL v3** 发布，完整条款见 [LICENSE](LICENSE)。保留上游及其他权利人的版权、许可和免责声明；新增修改不改变原代码的权利归属。不对 GPL 授予的商用、修改和再分发权利另加限制。
- **第三方组件**：独立第三方组件和素材仍遵循各自许可证及署名要求。根目录许可证不能代替第三方授权核验，也不授予上游或第三方的商标使用权。
- **无担保**：除适用法律或书面约定另有要求外，本软件按 GPL v3 所述不附带担保。请在部署前自行测试和备份。

### 本轮主要修改（截至 2026-10-26）

以下仅概述已知改动，不替代正式发布时的完整提交记录和变更清单：

- 增加 MySQL 适配，保留 SQLite / PostgreSQL 支持。
- 优化数据库迁移：版本记录、已完成迁移跳过、MySQL 元数据查询与 decimal 默认值比较，以及迁移进度日志。
- 调整 Orbit 用户端主题、登录表单卡片与分组输入区，并在 PC 胶囊导航中添加登录 / 个人中心入口。



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
./xmall                 # all    — HTTP server + background worker (default)
./xmall -mode api       # HTTP server only
./xmall -mode worker    # background worker only
```

Operator subcommands ship in the same binary, so a container needs no extra tooling:

```bash
./xmall admin list-admins
./xmall admin reset-password
./xmall admin reset-2fa
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

## 本二开版：配置与部署准备

当前提供可公开提交的 [config.yml.example](config.yml.example)。程序实际读取 `config.yml`，不会自动读取 demo。

- 仅在首次部署、确认不存在原配置时，将 `config.yml.example` 复制为 `config.yml`；不要覆盖正在使用的配置。
- 分别运行三次 `openssl rand -hex 32`，把三个不同的随机值填入 `app.secret_key`、`jwt.secret`、`user_jwt.secret`。这些字段在示例中为空，必须先填写，不能直接用于生产。
- 已有部署不要随意更换 `app.secret_key`，否则可能无法解密已有敏感数据。管理员初始化参数及第三方凭据仅在私有运行配置中填写。
- demo 默认监听 `127.0.0.1:8080`，使用 SQLite；容器部署需按需修改监听地址，其他部署按实际情况设置数据库、可信代理、CORS 和邮件等参数。
- 公开模板的 `update.repository` 默认是 `XManCodeing/XMall`，填写空字符串可关闭远程更新。修改模板不会修改既有运行配置。仓库改名后历史 Release 仍然保留，不代表旧附件兼容新的 `xmall_*` 更新器；须发布并验证 XMall 发行包后再使用更新功能，不要切到上游更新源。

### 内置 Telegram Bot 升级门禁

本版生产 worker 暂未装配旧 `bot:notify` 兼容路由；新版一旦消费旧任务就会 `SkipRetry` 归档，**不会等到执行 Bot 切换接口才停发**。首次升级必须进入维护窗口，先停止所有旧通知生产者，保留旧版 worker/接收端排空 pending、scheduled、retry、active 任务，确认在途事件完成且不再入队，再停止旧 worker 和外部 polling、备份数据库与 Redis、启动新版并完成显式接管。无法确认排空时不得升级；已归档或结果未知任务需人工核对，不删除队列或盲目重发。当前代码不自动证明排空，不能直接用后台一键重启代替此门禁。

Telegram 二维码默认按原始支付内容本地编码，不对 HTTP(S) 内容做网络探测。图片网关需在支付渠道配置 JSON 手动声明 `"telegram_qr_code_kind": "image"`，或由网关返回 `qrcode_type: "image"`；明确响应类型优先，未声明默认 `text`。图片须是受限公网 HTTP(S) PNG/JPEG/GIF（标准端口、同源重定向、8 秒超时、2 MiB、2048×2048），失败时仅回退到独立收银台链接。内嵌 PNG data URI 仍直接支持。不要提交真实渠道配置或密钥。

### 后台更新与重启

后台更新先下载、校验并替换二进制，再由管理员点击「立即重启」。重启方式按能力选择：

- systemd 托管且退出重启策略有效：优雅关闭后由 systemd 拉起；策略禁用或无法确认时，不绕过策略改用 exec。
- Linux 非 systemd 的原生二进制部署：通过可执行文件、架构、权限及启动上下文预检后，优雅关闭并用 exec 在原 PID 加载新程序。无需面板 API 或 sudo；不依赖面板周期巡检，但面板兼容性仍需在实际环境验证。
- 容器使用镜像更新与重建；其他不支持的环境保留手动重启指引。

重启会短暂中断服务，不是零停机升级。前端仅在实际运行版本匹配目标且不再待重启时刷新。预检失败不会主动停止旧服务；收尾或 exec 失败后可能已停服，需在管理器中检查日志并手动恢复。新版本启动失败不自动回滚数据库，迁移后回滚仍受安全检查约束。

从不支持 exec 的旧程序首次升级到支持版本，需要先手动重启一次。保留原启动参数、工作目录和运行身份；使用 aaPanel 等管理器时应在非生产环境验证更新、重启、日志和异常恢复，不将 exec 当作进程守护。

### GitHub Actions 云端打包与发布

正式发行无需本地构建。将源码上传到自己的 GitHub 仓库并启用 Actions 后：

1. 在 Actions 的 `goreleaser` 工作流选择 **Run workflow**：运行发布输入检查、安装脚本测试、后端检查与前端测试，然后构建所有配置的平台快照。下载工作流中的 snapshot artifact 检查包内容；手动运行不会创建 Release。
2. 修复检查发现的问题后，为通过验证的提交推送独立的 `v*` 版本 tag，例如 `v1.1.0`（请按自己的仓库版本序列选择）。若使用 `v1.1.0-rc.1` 等预发布版本并在 GitHub 标为 prerelease，安装器的 latest release 接口不会选中它。
3. tag 触发同样的检查并生成 **草稿 Release**，包含 Windows / Linux / macOS 的 amd64 / arm64 安装包、校验和，以及指向本仓库构建提交的源码、许可证和部署说明链接。
4. 检查对应源码、许可证和包内配置示例，并在目标系统验证安装 / 启动后，再手动公开草稿。草稿不供安装器的 latest release 接口下载。

发布 token 使用 Actions 自带的 `GITHUB_TOKEN`，不需要将个人 token 或运行配置写进仓库。工作流需要仓库允许 `contents: write`；仓库或组织策略可能需要管理员授权。

配置模板统一为 `config.yml.example`。GoReleaser 归档和 Docker 最终镜像均包含 `LICENSE`；构建前使用 `go mod tidy -diff` 检查依赖而不静默改写。本版安装脚本要求显式设置 `XMALL_UPDATE_REPOSITORY=XManCodeing/XMall`，不会默认下载上游。运行本仓库安装脚本时，在同一条 sudo 命令中通过 `env` 传入此变量；自行 fork 后应改为经过验证的自有发行仓库。

当前工作流只发布二进制安装包，**不会自动推送 Docker 镜像**。Dockerfile 已补许可证及可传入的 `SOURCE_URL`、`VCS_REF`、`APP_VERSION` 元数据参数；若以后发布镜像，需要另行配置镜像构建、推送及源码说明。本地检查不等于 GitHub Actions 和目标系统安装已通过。

## 上游部署文档（仅供参考，不安装本二开版）

> 下列安装器、Release 和 Docker 镜像链接均指向上游，安装得到的不是本目录的二开代码。保留这些说明是为了说明原项目的部署方式；部署本二开版应使用经过验证的自身发布物，或从本版源码构建。上游示例中的 `config.yml.example` 属于上游发布包，具体默认值可能与本工作副本不同。

### 上游官方一键安装器（Ubuntu / Debian）

On a fresh Ubuntu 22.04+ or Debian 12+ server, download and run the official
interactive installer:

```bash
curl -fsSL https://raw.githubusercontent.com/dujiao-next/dujiao-next/main/scripts/dujiao-next-manager.sh \
  -o /tmp/dujiao-next-manager.sh
sudo bash /tmp/dujiao-next-manager.sh install
```

The installer deploys the release binary with systemd, an isolated local Redis,
Nginx, SQLite, and a Let's Encrypt certificate. After installation, reopen the
management menu with:

```bash
sudo dujiao-next-manager
```

Common automation-friendly commands are also available:

```bash
sudo dujiao-next-manager status
sudo dujiao-next-manager logs app
sudo dujiao-next-manager restart
sudo dujiao-next-manager configure-domain
sudo dujiao-next-manager configure-admin-path
sudo dujiao-next-manager renew-cert
sudo dujiao-next-manager admin-reset-password
sudo dujiao-next-manager admin-reset-2fa
sudo dujiao-next-manager uninstall
```

The first release supports a single non-wildcard domain on Ubuntu/Debian only.
It does not adopt an existing manual installation. If SMTP is skipped, configure
it in the admin panel before enabling email-verification registration. Application
data lives in `/opt/dujiao-next`; installer state is stored in
`/etc/dujiao-next/install-state.json`. TLS failures leave only the ACME challenge
endpoint enabled, and `install` can be rerun after DNS or firewall repair. Safe
uninstall creates and verifies a `0600` recovery archive under
`/var/backups/dujiao-next` before deleting managed data.

### 上游二进制安装（非本二开版）

Download the latest `dujiao-next_*.tar.gz` from [Releases](https://github.com/dujiao-next/dujiao-next/releases):

```bash
tar -xzf dujiao-next_*.tar.gz
cp config.yml.example config.yml
# edit config.yml: set app.secret_key, jwt.secret, user_jwt.secret, and web.admin_path
./dujiao-next
```

Full instructions: https://dujiao-next.com/deploy/

Or with the upstream Docker image (not this modified edition):

```bash
docker run -d -p 8080:8080 -v $PWD/config.yml:/app/config.yml:ro dujiaonext/dujiao-next:latest
```

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
go build -tags release,fullstack -o xmall ./cmd/server
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
