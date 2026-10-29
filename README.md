# XMall

XMall 是面向数字商品销售的全栈商城，包含 Go 后端、用户商城、管理后台和内置 Telegram Bot。正式发行包将两个前端嵌入同一个可执行文件，可通过单二进制部署，也可从源码构建 Docker 镜像。

本项目基于 Dujiao-Next 持续二次开发，重点改进数据库兼容性、Telegram 购物与履约、用户端体验，以及部署、更新和运维流程。XMall 为独立维护的非官方衍生版本。

[源码仓库](https://github.com/XManCodeing/XMall) · [版本下载](https://github.com/XManCodeing/XMall/releases) · [问题反馈](https://github.com/XManCodeing/XMall/issues) · [许可证](LICENSE)

## 功能概览

- **商城交易**：商品与 SKU、订单、支付、钱包、优惠券、卡密及自动履约。
- **业务管理**：商品与库存、采购与上游对接、分销、会员及运营配置。
- **用户体验**：多套商城模板、简体中文 / 繁体中文 / 英文、账号及第三方登录。
- **后台管理**：独立管理员认证、角色权限控制、TOTP 双重验证。
- **Telegram**：内置 Bot 实例管理、购物与支付、订单查询与交付、通知及群发。
- **部署运维**：SQLite / MySQL / PostgreSQL，单进程或 API / worker 分离运行，后台更新与重启。

## XMall 的主要改进

以下是本版的重点改动；具体发行版本包含哪些功能，以对应 Release 和提交记录为准。

### 数据库兼容与迁移

- 增加 MySQL 适配，保留 SQLite 和 PostgreSQL 支持。
- 数据库迁移引入版本记录，跳过已完成步骤并输出进度日志。
- 优化 MySQL 元数据查询及 decimal 默认值比较，减少重复迁移。

### 内置 Telegram Bot

- 将 Bot 运行、购物、支付和交付接入商城后端，不必另行运行外置购物机器人。
- 增加实例管理、运行状态、菜单配置、群发及逐条投递状态查询。
- 通过分布式租约、续租和取消链路控制多实例执行；发送结果不确定时保留待核对状态，避免盲目重发。
- 区分确定未提交网关与结果未知的支付失败，支持支付二维码原始内容及明确标注的二维码图片。
- 增加旧 Bot 显式导入与接管流程，持久保存历史 Bot 身份；清空 Token、更换 Bot 或软删除渠道不能绕过接管检查。

### 用户端与运维体验

- 调整 Orbit 模板、登录表单及分组输入布局，补充 PC 导航中的登录和个人中心入口。
- 统一 XMall 安装包、安装管理脚本和更新源，避免误装其他项目的发行物。
- 改进后台更新流程：替换二进制后显式重启；支持 systemd 托管重启及符合条件的 Linux 原生进程 exec 重启。
- 提供 GitHub Actions 构建与草稿发布流程，发行包包含配置示例和许可证。

## 部署前准备

- **使用发行包**：无需安装 Go、Node.js 或前端构建工具。
- **数据库**：默认 SQLite；需要独立数据库时可配置 MySQL 或 PostgreSQL。
- **Redis**：基础服务可在关闭队列时运行；内置 Telegram Bot 和异步任务需要启用 `queue.enabled` 并配置可用的 Redis。`redis` 与 `queue` 是独立配置项。
- **公网访问**：准备域名、HTTPS 和反向代理，确认支付回调等地址能被外部访问。
- **已有部署**：先备份数据库、运行配置、上传文件，以及使用中的 Redis 队列；不要用新模板覆盖原配置。

### 配置文件

程序读取 `config.yml`；公开模板为 [config.yml.example](config.yml.example)。仅首次部署、配置尚不存在时复制：

```bash
cp -n config.yml.example config.yml
chmod 600 config.yml
```

编辑自己的 `config.yml`，至少确认以下项目：

- `app.secret_key`、`jwt.secret`、`user_jwt.secret`：分别运行一次 `openssl rand -hex 32`，填入三个不同随机值。已有部署不要随意更换 `app.secret_key`，否则可能无法解密存量数据。
- `bootstrap.default_admin_username`、`bootstrap.default_admin_password`：首次初始化的管理员账号与强密码。
- `server.host`、`server.port`：模板默认 `127.0.0.1:8080`；容器内应将监听地址改为 `0.0.0.0`。
- `database`：数据库类型、连接信息及连接池；SQLite 保持单连接。
- `web.admin_path`：后台路径，默认 `/admin`。
- `server.trusted_proxies`、`cors.allowed_origins`：按实际代理与访问域名设置。
- `email`：需要邮件验证码等功能时配置 SMTP。
- `update.repository`：本项目为 `XManCodeing/XMall`；空字符串关闭远程更新。自行 fork 应指向自己的可信发行仓库。

不要提交运行配置、任何 `.env*`、密钥、数据库、日志或用户数据。

## 部署方式

### 方式一：发行包部署

从 [XMall Releases](https://github.com/XManCodeing/XMall/releases) 下载与系统、CPU 架构匹配的 `xmall_*` 安装包，并核对发布页提供的校验和。Linux / macOS 使用 `.tar.gz`，Windows 使用 `.zip`。请选择已公开的 XMall 发行物，不使用旧品牌附件代替。

解压至独立目录，按上文配置 `config.yml` 后，在该目录启动：

```bash
./xmall
```

Windows 使用 `xmall.exe`。首次启动会执行必要的数据库迁移；默认配置下可访问：

- 商城：`http://127.0.0.1:8080/`
- 后台：`http://127.0.0.1:8080/admin/`
- 健康检查：`http://127.0.0.1:8080/health`

生产环境应使用 systemd 或其他进程管理器保持运行，并通过反向代理提供 HTTPS。固定程序工作目录和运行身份，持久保存 `config.yml`、`db/`、`uploads/`；日志默认位于 `logs/`。

### 方式二：Linux 安装管理脚本

适用于 **Ubuntu 22.04+ / Debian 12+、systemd、amd64 / arm64** 的新部署。脚本配置 XMall、独立 Redis、Nginx、SQLite 和 HTTPS；不接管已有手动安装。开始前确认域名解析及 80 / 443 端口可用。

从本仓库取得源码后，先审阅 [scripts/xmall-manager.sh](scripts/xmall-manager.sh)，再执行：

```bash
sudo env XMALL_UPDATE_REPOSITORY=XManCodeing/XMall \
  bash scripts/xmall-manager.sh install
```

安装器要求所选仓库已有可下载的正式 Release；草稿和预发布版本不会被 latest release 接口选中。没有符合要求的发行包时，先完成发布或使用源码构建，不切换到其他项目的更新源。

安装后常用命令：

```bash
sudo xmall-manager status
sudo xmall-manager logs app
sudo xmall-manager restart
sudo xmall-manager configure-domain
sudo xmall-manager configure-admin-path
sudo xmall-manager admin-reset-password
sudo xmall-manager admin-reset-2fa
```

应用目录为 `/opt/xmall`，安装状态位于 `/etc/xmall`，卸载备份位于 `/var/backups/xmall`。重新执行安装流程时仍需显式传入 `XMALL_UPDATE_REPOSITORY`。

### 方式三：自行构建 Docker 镜像

仓库提供 [Dockerfile](Dockerfile)。现有安装包发行流程不变；新增的 [Docker 内测工作流](.github/workflows/docker-internal.yml) 仅手动运行，暂不发布公共镜像，也不绑定当前版本标签。普通部署仍从本项目源码构建：

```bash
docker build --build-arg APP_VERSION=local -t xmall:local .
```

先准备宿主机 `config.yml`，将 `server.host` 改为 `0.0.0.0`，保持端口 `8080`；然后运行：

```bash
docker run -d --name xmall --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  --mount type=bind,src="$(pwd)/config.yml",dst=/app/config.yml,readonly \
  --mount type=volume,src=xmall-db,dst=/app/db \
  --mount type=volume,src=xmall-uploads,dst=/app/uploads \
  --mount type=volume,src=xmall-logs,dst=/app/logs \
  xmall:local
```

此示例供宿主机反向代理访问，不直接公开应用端口。Redis 和外部数据库需另行提供，容器内的 `127.0.0.1` 不指向宿主机。容器升级使用新镜像重建，保留数据卷，不使用容器内二进制自更新。

## Telegram 启用与旧版迁移

新部署先配置 Redis 队列，再通过后台管理 Bot 实例、Token、菜单及相关业务配置。不要让同一 Bot 的外部 polling 程序与内置实例同时运行。

**已有外置 Bot 的部署不能直接启用内置实例代替迁移：**

1. 进入维护窗口，停止旧通知生产者，保留旧 worker 与接收端排空 `bot:notify` 的 pending、scheduled、retry、active 任务，并确认在途事件结束、不再入队。
2. 停止旧 worker 和外部 polling，备份数据库与 Redis，再启动新版并执行显式导入、凭据撤销及接管。
3. 核对未完成交易与通知后再恢复业务。新版 worker 未装配旧通知兼容路由，消费旧任务会归档；不能用删除队列或直接后台重启替代排空。

v5 迁移保留旧渠道的历史身份。空、非法或无法解密的历史会进入 `unresolved` 隔离，不会因填写新 Token 自动放行。管理员需先禁用渠道、核实完整历史，再通过渠道更新接口提交 `history_bot_ids`、最新 `history_revision` 和 `confirm_complete_history: true`；不得混入普通配置字段。无法确认历史就保持隔离。软删除的未知历史目前没有管理界面或现成 CLI，需专项运维处理。

支付二维码默认按原始内容本地编码，不探测普通支付 URL。返回图片地址的网关需在渠道配置 JSON 声明 `"telegram_qr_code_kind": "image"`，或返回 `qrcode_type: "image"`；响应中的明确类型优先。图片获取失败仅回退到独立收银台链接，不把图片地址当成支付码。

## 更新、备份与常用运维

### 更新流程

1. 阅读目标版本说明，备份数据和配置；涉及旧 Bot 接管时先完成上述维护门禁。
2. 使用本项目发行包或后台更新功能，核对目标版本及更新源。
3. 后台更新替换二进制后，点击「立即重启」。systemd 部署遵循其重启策略；符合预检条件的 Linux 非 systemd 原生部署使用 exec；其他环境手动重启。
4. 检查 `/health`、实际版本、后台登录、订单及任务处理，再恢复业务。

重启会短暂中断服务，不是零停机升级。旧程序首次升级到支持 exec 的版本时需要先手动重启。更新失败或新版本启动失败不会自动回滚数据库；不要仅替换旧二进制就假定迁移已撤销。

### 运行模式与管理员恢复

```bash
./xmall                  # 默认：HTTP 服务 + worker
./xmall -mode api        # 仅 HTTP 服务
./xmall -mode worker     # 仅后台 worker，需要启用队列

./xmall admin list-admins
./xmall admin reset-password
./xmall admin reset-2fa
```

多进程部署须保持数据库、队列及加密密钥一致。备份不仅包含程序文件：SQLite 需使用一致性备份方式，独立数据库使用对应备份工具，启用队列时同时考虑 Redis 状态；恢复前核对支付及发送结果，避免重复执行。

## 本地开发

技术栈：Go 1.26.5、Gin、GORM；Vue 3、TypeScript、Vite、Tailwind CSS v4；Redis / asynq；JWT、Casbin 和 TOTP。前端工具链与 CI 对齐为 Node.js 24.11.1、pnpm 10.34.3，可通过 Corepack 启用 pnpm。

准备私有 `config.yml`。**当前两个前端开发代理均指向 `127.0.0.1:8088`**，开发时将后端 `server.port` 设为 `8088`，并按需允许前端地址跨域。分别在三个终端运行：

```bash
# 仓库根目录：API 服务，不内嵌前端
go run ./cmd/server
```

```bash
cd frontend/user
pnpm install --frozen-lockfile
pnpm run dev
# 用户端：http://localhost:6173
```

```bash
cd frontend/admin
pnpm install --frozen-lockfile
pnpm run dev
# 管理后台：http://localhost:6174
```

### 目录与开发约定

- `cmd/server/`：服务入口及管理员运维命令。
- `internal/modules/`：按业务模块划分的领域、应用、接口和基础设施实现。
- `internal/app/`、`internal/bootstrap/`：依赖装配、路由和后台任务。
- `internal/architecture/`：模块依赖和代码结构约束测试。
- `frontend/user/`、`frontend/admin/`：用户商城与管理后台。
- `internal/web/`：前端嵌入与路由挂载。
- `scripts/`、`.github/workflows/`：安装、检查与发布脚本。

后台路径由 `web.admin_path` 在运行时决定，前端原生链接使用现有 `adminUrl()` 工具。新增后台接口需同步维护角色权限及覆盖测试。数据库事务内使用事务绑定的存储，不调用全局数据库连接；SQLite 单连接下混用会导致等待。

### 构建与测试

仅构建 API 服务：

```bash
go build -o xmall ./cmd/server
```

构建含两个前端的本机快照，使用 GoReleaser v2（需已安装 Go、Node.js 和 pnpm）：

```bash
goreleaser build --snapshot --single-target --clean
```

构建配置自动安装锁定依赖、构建两个前端并嵌入。后台必须使用 `build:fullstack`，不能以普通 `build` 代替；直接使用 `-tags release,fullstack` 编译前必须先准备两份前端产物。

常用检查：

```bash
go mod tidy -diff
go vet ./...
go test ./...
go test -race ./internal/...
(cd frontend/admin && pnpm test && pnpm run build:fullstack)
(cd frontend/user && pnpm test && pnpm run build)
```

本地测试不替代真实支付、Telegram Bot、目标数据库及服务器环境的验收。

## 构建与发布

[GitHub Actions 发布工作流](.github/workflows/release.yml) 和 [GoReleaser 配置](.goreleaser.yaml) 是本项目发行流程的依据：

1. 手动运行 `goreleaser` 工作流，执行输入检查、安装脚本检查、后端与前端测试，生成快照附件，不创建正式 Release。
2. 对通过验证的提交推送独立 `v*` tag，生成包含 Linux / Windows / macOS、amd64 / arm64 产物和校验和的草稿 Release。
3. 核对目标系统安装与启动、版本信息、源码及许可证后，手动公开草稿。

工作流使用 `GITHUB_TOKEN`，需要 `contents: write` 权限。当前不自动发布 Docker 镜像。发行物应对应明确 tag / commit，并提供完整对应源码、构建材料和许可文件；运行密钥和业务数据不属于发行材料。

### Docker 私有内测（维护者）

在 Actions 中手动运行 `docker-internal`，默认选择 `validate`：

- 构建 Linux `amd64` / `arm64` 镜像，在 amd64 上检查启动、`/health`、商城和自定义后台路径，并用同一 SQLite 数据卷重建容器检查再次启动。
- 不登录 GHCR、不上传镜像或镜像归档；临时产物仅留在运行器，结束后清理。公开仓库的 Actions 日志仍公开，不要将运行配置或生产凭据作为输入。
- 不改版本号、Git 标签、Release 附件或 `latest`；`release.yml` 安装包流程保持独立。

`private` 模式只允许从 `main` 运行，验证通过后向 `ghcr.io/<owner>/xmall-internal` 推送唯一的 `internal-<commit>-<run>-<attempt>` 标签。必须事先存在该 **private** GHCR 包，并授予本仓库 Actions 访问权限；不存在、无权限或可见性不为 private 时直接停止，不自动创建包、不自动改可见性。使用工作流的 `GITHUB_TOKEN`，无需把个人 Token 写入仓库。此入口用于已有私有包的后续推送；首次建包及私有访问权限需单独确认，不能把构建验证成功当作已验证推送。

当前未启用 `release.published` 触发，也不生成正式版本或 `latest` 镜像标签。内测通过并确认公开发布后，再接入正式 Release 发布事件，并固定到该 Release 标签对应的提交构建。

## 致谢与许可证

感谢 [Dujiao-Next](https://github.com/dujiao-next/dujiao-next) 及其贡献者提供的项目基础，也感谢所使用的开源组件。原项目资料可参考 [上游文档](https://dujiao-next.com)，XMall 的部署与行为以本仓库和对应发行版本为准。

本工作副本自 2026-10-26 起记录二次开发修改，主要改动见上文及提交历史；原始导入的准确上游 tag / commit 尚待核验，不将来源目录名视为已确认版本。XMall 不代表上游维护者或其合作伙伴。

本衍生项目依照 **GNU GPL v3** 发布，详见 [LICENSE](LICENSE)。保留上游及其他权利人的版权、许可和免责声明；第三方组件遵循各自许可证。发布修改版或构建产物时须提供相应源码与许可材料。本软件不附带担保。
