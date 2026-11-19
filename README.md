# bps-local

**QQ 交流群：`1135421637`** · 项目使用与问题交流。

将本机的 Responses API 请求转换为 ChatGPT Excel / Basispoints（BPS）请求的 Go 命令行桥接器。客户端通过 HTTP 发送请求、通过 SSE 接收事件；桥接器通过 WebSocket 连接 BPS 上游。

```text
Responses 客户端 → http://127.0.0.1:8790/v1/responses → BPS WebSocket
```

本仓库只发布独立 CLI，不包含 CPA 插件或管理服务。BPS 属于产品内部接口，本项目并非 OpenAI 官方项目；接口、授权方式和模型权限可能变化，代码可构建不代表目前的上游请求一定成功。

## 快速开始

安装 Go 1.26.0 或更高版本，进入本目录构建：

```bash
go build -o bps-local ./cmd/bps-local
```

以下示例使用本机 `7890` 代理端口，请替换成自己的代理地址。先完成独立 BPS 授权，默认使用设备码登录：

```bash
./bps-local --login --proxy http://127.0.0.1:7890
```

终端显示设备码并打开授权页面，由你完成登录与确认。默认保存到 `~/.bps-local/oauth.json`；若文件已存在，程序拒绝覆盖，可用 `--auth` 指定另一个文件。程序不会自动读取或修改 Codex 的授权文件。

也可以使用普通浏览器授权，无需输入设备码。需要已安装 Chrome、Chromium 或 Edge：

```bash
./bps-local --login --login-method browser \
  --auth "$HOME/.bps-local/browser-oauth.json" \
  --proxy http://127.0.0.1:7890
```

程序打开独立的临时浏览器窗口，由你完成登录、验证码和授权；成功后自动保存凭据。它不读取已有 Chrome、Excel 或 Codex 的登录信息，结束时关闭本次窗口并清理临时 profile。无法自动找到浏览器时，用 `--browser-path` 指定其可执行文件路径。浏览器模式仅支持无用户名密码的代理；带认证的上游代理需先通过本地转发使用。

无桌面的 Linux 使用设备码模式加 `--no-browser`，在其他电脑打开终端输出的网址完成授权。`--login-method browser` 不支持 `--no-browser`。详细步骤见 [独立授权](docs/USAGE.md#2-独立授权)。

启动桥接器，保持该终端运行：

```bash
./bps-local --proxy http://127.0.0.1:7890
```

若登录时指定了另一个 OAuth 文件，启动时也需指定同一个 `--auth` 路径。

在另一终端调用本地接口。将 `YOUR_MODEL` 换成该账号实际有权使用的模型：

```bash
curl -N http://127.0.0.1:8790/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"YOUR_MODEL","input":"只回复 OK","stream":true,"reasoning":{"effort":"medium"}}'
```

本地接口仅监听回环地址，不要求客户端提供 API Key。它返回流式事件；应检查最终事件和内容，HTTP 连接成功不等于生成成功。

### 单进程多账号（按 API Key 路由）

用 [accounts.example.json](accounts.example.json) 作为模板配置 `accounts.json`，每个账号只需指定一份自己的 `auth` 文件和一把独立的 `api_key`。账号按数组顺序使用 `account-1`、`account-2` 等内部状态目录；`id` 不需要配置。例如用 `openssl rand -hex 32` 分别生成两把 Key；**这里填的是本地 API Key，不是 OpenAI API Key 或 OAuth Access Token**。相对 `auth` 路径以 `accounts.json` 所在目录为准。将 `accounts.json` 和 OAuth 文件设为 `600`，不要提交或分享。

```bash
./bps-local --accounts ./accounts.json
```

各客户端使用同一个地址 `http://127.0.0.1:8790/v1`，用 `Authorization: Bearer <api_key>` 选定账号。缺失或错误的 Key 返回 `401`，不会回退到其他账号。每个账号有独立的任务状态和队列；`--proxy` 使用共同的出口。`/healthz` 仅检查进程是否存活，不验证各账号的 OAuth。配置更改后重启服务。此前的 `--auth` 单账号模式不受影响。

## Docker（Linux）

GitHub Actions 在推送 `main` 或 `v*` 标签时构建并发布 `linux/amd64`、`linux/arm64` 镜像到 `ghcr.io/sunboyzoey/bps-local-public`。`main` 更新 `latest` 和 `sha-<提交 SHA>`，版本标签保留原名。面向 `main` 的 PR 只验证构建，不发布。首次发布后，如需让其他人匿名拉取，请在 GitHub Package 设置中确认可见性为 Public。

在宿主机完成授权，再把凭据文件只读挂载进容器。示例在 Linux 上使用宿主网络，使服务继续只监听 `127.0.0.1:8790`，也能访问宿主机的回环代理。不要加 `-p`；默认桥接网络无法通过端口映射访问容器内只监听回环的服务。

```bash
mkdir -p "$HOME/.bps-local/docker-state"
docker run --rm --network host --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$HOME/.bps-local/browser-oauth.json,dst=/run/bps-oauth.json,readonly" \
  --mount "type=bind,src=$HOME/.bps-local/docker-state,dst=/state" \
  ghcr.io/sunboyzoey/bps-local-public:latest \
  --auth /run/bps-oauth.json --state-dir /state --proxy http://127.0.0.1:7890
```

授权保存到了其他文件时，替换第一个挂载的 `src`。没有本地代理时，省略 `--proxy`。镜像内没有浏览器，不支持在容器内进行浏览器授权。

### Docker Compose 多账号

需要一个端口按 Key 路由时，用 [compose.accounts.yaml](compose.accounts.yaml)。保持 `accounts.json`、`auth-a.json`、`auth-b.json` 和 Compose 文件同目录；普通 Linux Docker 上，默认镜像用户 UID `65532` 必须能读取这三个 `600` 文件。然后运行 `docker compose -f compose.accounts.yaml up -d --build`。增加账号时增添配置项和对应的只读凭据挂载。它与下方的多端口配置默认都使用 `8790`，不要同时启动，或用 `BPS_PORT` 给按 Key 路由的容器换一个端口。

### Docker Compose 多端口

[compose.yaml](compose.yaml) 在一份配置里启动两个容器：`bps-a` 监听 `127.0.0.1:8790`，`bps-b` 监听 `127.0.0.1:8791`。每个账号有自己的 OAuth 文件和 Docker 状态卷，客户端按端口选账号；没有账号轮询或统一入口。

在 Linux 服务器上把两份独立授权文件放在 `compose.yaml` 同目录，命名为 `auth-a.json` 和 `auth-b.json`。Compose 使用默认镜像的非 root 用户（UID `65532`）。以下命令适用于未启用用户命名空间映射的 Linux Docker：

```bash
sudo chmod 600 auth-a.json auth-b.json
sudo chown 65532 auth-a.json auth-b.json
docker compose up -d --pull always
docker compose ps
curl -fsS http://127.0.0.1:8790/healthz
curl -fsS http://127.0.0.1:8791/healthz
```

Docker 自动创建并持久化状态卷，不用在宿主机上创建或修改状态目录的权限。不要使用 `docker compose down -v` 删除状态卷。从旧版绑定宿主机目录的配置切换时，旧 `.bps-local/state-*` 不会被删除，也不会自动导入新卷；如需保留原有 task 身份，请先迁移原来的 `task.key`，否则新卷会生成新的 task 身份。OAuth 凭据不受影响。

`auth-*.json` 和 `.bps-local/` 已被 Git 忽略，不要强制添加或提交。只启动一个账号用 `docker compose up -d bps-a`，此时只需要 A 的凭据文件。如果已有服务占用端口，设置 `BPS_PORT_A` / `BPS_PORT_B` 为不同的可用端口。需要代理时设置 `BPS_PROXY`（共用）或 `BPS_PROXY_A` / `BPS_PROXY_B`（单独覆盖），地址必须能从 Linux 服务器访问。变量可以写在 Compose 的 `.env` 中，但不要在里面存放 OAuth 内容。增加账号时复制一个 `services` 条目，并使用独立服务名、端口、`auth-*.json` 文件和状态卷。替换某账号的凭据文件后，重新设置属主和权限，再用 `docker compose up -d --force-recreate bps-a` 重建对应容器以重新挂载，其他账号不受影响。

## 使用范围

- 支持 Responses 文本输入、客户端工具调用转换，以及上游事件到本地 SSE 的转换。
- 默认进程使用一份独立 OAuth 文件；`--accounts` 可按本地 API Key 固定路由。没有多账号管理界面、账号轮询或模型列表接口。
- 使用固定 task 槽位池：默认每账号并发 `4`、等待队列 `64`、排队及上游启动等待预算 `30s`。这些是本地配置，不代表上游允许的并发。
- 可选 `--upstream-rps` 分别限制每账号的 WebSocket 拨号和 `response.create` 启动速率，默认 `0`（关闭限速，仍保留并发限制）。握手安全重试也计入拨号限制。
- 每次请求自带完整上下文，不跨客户端追加历史；不支持 `previous_response_id`。复用 task 槽位不会复用调用方的对话历史。
- task 身份保存在 `~/.bps-local/local-state`，重启沿用；同一状态目录同时只允许一个服务进程使用。
- 独立 CLI 的图片输入使用 HTTPS 图片 URL；不提供内联图片的公开托管地址。
- 不执行 Excel 工作簿操作，不提供上游托管的搜索、文件搜索或图像生成工具。
- OAuth 文件可保存 Access Token、Refresh Token 和有效期等元数据，但 **CLI 不自动使用 Refresh Token 刷新授权**。登录完成和 `--check` 均不验证真实生成权限。

Windows 构建使用 `go build -o bps-local.exe ./cmd/bps-local`。附带的启动脚本默认代理是 `12450`，使用 `7890` 时请显式覆盖：

```powershell
.\bps-local.exe --login --proxy http://127.0.0.1:7890
.\bps.ps1 -Proxy http://127.0.0.1:7890
```

完整参数、上下文示例及故障排查见 [使用说明](docs/USAGE.md)。上游只支持 `ws`；`--upstream-transport sse` 会被拒绝，本地 Responses 接口仍返回 SSE。

## 升级与状态目录

停止旧进程后替换二进制，保留原 OAuth 文件和 `--state-dir` 目录。首次启动会创建 `task.key` 与 `owner.lock`；后续重启请沿用它们，不要把该目录当作缓存清理。状态目录不保存 OAuth 凭据或对话历史。

需要同时启动多个实例时，为每个实例指定不同的 `--addr` 和独立的 `--state-dir`。并发和限速均在各进程内生效，不会跨进程合并。同一个状态目录被占用时，新进程会明确拒绝启动。

例如，在默认并发下启用每秒两次的启动限制：

```bash
./bps-local --proxy http://127.0.0.1:7890 --upstream-rps 2
```

发布包由 `scripts/package.sh` 仅构建 `./cmd/bps-local`，附带使用文档、许可、占位凭据示例及 Windows 启动脚本。

## 许可与来源

项目许可、第三方声明及依赖许可原文统一见根目录 [LICENSE](LICENSE)。

## 社区

- [LINUX DO](https://linux.do/) · 项目发布与交流。
