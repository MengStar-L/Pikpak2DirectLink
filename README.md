<h1 align="center">PikPak2DirectLink</h1>

<p align="center">
  <strong>把磁力链接和 PikPak 分享链接，安静、可靠地转换成你的下载链接。</strong>
</p>

<p align="center">
  <a href="https://github.com/MengStar-L/Pikpak2DirectLink/releases/latest">
    <img alt="Release" src="https://img.shields.io/github/v/release/MengStar-L/Pikpak2DirectLink?style=for-the-badge&label=release&color=ff6b35">
  </a>
  <img alt="Go" src="https://img.shields.io/badge/go-1.26.5-334155?style=for-the-badge&logo=go&logoColor=white">
  <img alt="Self Hosted" src="https://img.shields.io/badge/self--hosted-prod-111827?style=for-the-badge">
  <img alt="Proxy" src="https://img.shields.io/badge/proxy-token-0ea5e9?style=for-the-badge">
</p>

<p align="center">
  <img alt="Platforms" src="https://img.shields.io/badge/platforms-linux%20amd64%20%7C%20linux%20arm64%20%7C%20windows%20amd64%20%7C%20darwin-14b8a6?style=for-the-badge">
</p>

<p align="center">
  PikPak2DirectLink 是一个轻量的私有直链面板。它通过 PikPak 账号的离线下载、分享转存和文件下载能力，把磁力链接或 PikPak 分享链接解析为直链或服务端代理链接，并提供账号池、并行队列、CDK 用户入口、在线更新和 aria2 推送。
</p>

<p align="center">
  <a href="https://github.com/MengStar-L/Pikpak2DirectLink/releases/latest">下载最新版</a>
  ·
  <a href="CHANGELOG.md">查看发布记录</a>
  ·
  <a href="https://github.com/MengStar-L/Pikpak2DirectLink/issues">反馈问题</a>
</p>

---

> 本项目依赖 PikPak 非官方接口行为。请只解析你有权访问的资源，并妥善保护自己的账号、密码和服务器。

## 快速开始

下面以 Linux x86_64 服务器为例。默认安装目录为 `/opt/Pikpak2DirectLink`，服务默认只监听 `127.0.0.1:51873`。

```bash
sudo apt update
sudo apt install -y curl ca-certificates openssl

sudo mkdir -p /opt/Pikpak2DirectLink
sudo chown -R "$USER:$USER" /opt/Pikpak2DirectLink
cd /opt/Pikpak2DirectLink

curl -L -o Pikpak2DirectLink \
  https://github.com/MengStar-L/Pikpak2DirectLink/releases/latest/download/Pikpak2DirectLink_linux_amd64
chmod +x Pikpak2DirectLink

umask 077
test -s .data-encryption-key || openssl rand -base64 32 > .data-encryption-key
export DATA_ENCRYPTION_KEY="$(tr -d '\r\n' < .data-encryption-key)"
./Pikpak2DirectLink
```

先在服务器本机确认进程可访问：

```bash
curl http://127.0.0.1:51873/api/auth/status
```

首次初始化不要先开放反向代理。服务器没有桌面环境时，在自己的电脑上建立 SSH 隧道：

```bash
ssh -L 51873:127.0.0.1:51873 your-user@your-server
```

启动日志会输出一条包含 `#setup_token=...` 的初始设置 URL；systemd 部署可用 `journalctl -u Pikpak2DirectLink -n 50` 查看。保持隧道连接，在本机浏览器打开这条完整 URL 并设置管理员密码。URL 中的 token 只保存在内存中、启动 30 分钟后过期，过期后重启服务即可生成新 URL。

初始化完成后再关闭隧道、配置 HTTPS 反向代理及 `PUBLIC_BASE_URL=https://你的域名`，然后通过域名访问管理后台。不要把初始设置 URL 发给其他人，也不要经公网反向代理打开它。

> `DATA_ENCRYPTION_KEY` 是必填的 32 字节 Base64 密钥。首次生成后必须长期保留，后续启动继续使用同一密钥；丢失或误换密钥会导致已保存的账号凭据、session 和任务详情无法解密。请把密钥与数据库备份分开保管，不要提交到 Git。

> ARM64 服务器请把下载文件名改为 `Pikpak2DirectLink_linux_arm64`。不确定架构时可运行 `uname -m`：`x86_64` 对应 `amd64`，`aarch64` 对应 `arm64`。

## 功能概览

| 能力 | 说明 |
| --- | --- |
| 链接解析 | 支持磁力链接和 PikPak 分享链接，输入时自动识别类型。 |
| 多链接批量解析 | 输入框可粘贴多行链接，每行作为独立任务进入全局队列，结果按文件树展示。 |
| 多账号账号池 | 支持添加多个 PikPak 账号；账号失败时自动切换，session 失效时可用已保存凭据重新登录。 |
| 会员与流量管理 | 账号页显示会员状态、会员到期时间、本月下行流量和每月流量额度，默认每个账号 700G。 |
| 并行解析队列 | 后台可设置同时解析数量；并行模式会在可用账号间轮转，降低单账号压力。 |
| 文件选择 | 分享链接或解析结果包含多个文件时，可在页面中选择目标文件；CDK 用户支持多选生成链接。 |
| 直链与代理 | 解析完成后同时提供 PikPak 直链和服务端代理链接，代理链接带唯一令牌。 |
| CDK 分发 | 管理员可生成带流量额度和有效期的 CDK，用户通过 `/u` 入口自助解析。 |
| aria2 推送 | 管理后台和 CDK 用户入口都支持把单个或多个链接推送到浏览器配置的 aria2 RPC。 |
| 实时日志 | 日志页展示账号尝试、文件检测、直链获取、临时文件清理和更新过程。 |
| 访问保护 | 访问门始终开启；可首次访问设置管理员密码，也可用 `ACCESS_PASSWORD` 固定密码。 |
| 在线更新 | 后台定时检查 GitHub Releases，可在「更新」页面一键下载、校验并安装新版本。 |

## 安装与部署

PikPak2DirectLink 主要面向 Linux 服务器运行。你可以直接使用预编译二进制，也可以从源码构建。

### 方式一：下载预编译二进制

[Releases](https://github.com/MengStar-L/Pikpak2DirectLink/releases) 会发布多个平台的二进制文件，命名规则为：

```text
Pikpak2DirectLink_<os>_<arch>
Pikpak2DirectLink_windows_amd64.exe
```

常见 Linux 文件：

- `Pikpak2DirectLink_linux_amd64`
- `Pikpak2DirectLink_linux_arm64`

下载后无需安装 Go 环境，赋予执行权限即可运行。

### 方式二：从源码构建

需要本机安装 Go。以下示例以 Debian/Ubuntu x86_64 为例：

```bash
sudo apt update
sudo apt install -y git curl ca-certificates openssl

curl -LO https://go.dev/dl/go1.26.5.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.5.linux-amd64.tar.gz
rm -f go1.26.5.linux-amd64.tar.gz

sudo mkdir -p /opt/Pikpak2DirectLink
sudo chown -R "$USER:$USER" /opt/Pikpak2DirectLink

git clone https://github.com/MengStar-L/Pikpak2DirectLink.git /opt/Pikpak2DirectLink
cd /opt/Pikpak2DirectLink

/usr/local/go/bin/go build \
  -ldflags "-X pikpak2directlink/internal/version.Version=$(git describe --tags --always)" \
  -o Pikpak2DirectLink ./cmd/server
```

`-ldflags` 会把版本号写入二进制，在线更新功能会用它和 GitHub 最新 Release 比较。不带该参数时版本显示为 `dev`，程序仍可正常运行。

### 直接运行

```bash
cd /opt/Pikpak2DirectLink
umask 077
test -s .data-encryption-key || openssl rand -base64 32 > .data-encryption-key
export DATA_ENCRYPTION_KEY="$(tr -d '\r\n' < .data-encryption-key)"
./Pikpak2DirectLink
```

如需修改监听地址或端口：

```bash
ADDR=127.0.0.1:8080 ./Pikpak2DirectLink
```

只有在完成管理员初始化并配置好防火墙或受信任的反向代理后，才应把 `ADDR` 改为非 loopback 地址。

### systemd 自启动

推荐用 systemd 托管服务，便于开机自启、查看日志和在线更新后自动重启。下面会优先复用「快速开始」已经生成的 `.data-encryption-key`；只有尚未生成密钥时才创建新密钥，并且不会覆盖已有的 `server.env`：

```bash
sudo install -d -m 700 /etc/Pikpak2DirectLink
if [ -s /opt/Pikpak2DirectLink/.data-encryption-key ]; then
  DATA_KEY="$(tr -d '\r\n' < /opt/Pikpak2DirectLink/.data-encryption-key)"
else
  DATA_KEY="$(openssl rand -base64 32)"
fi
sudo sh -c 'test -s /etc/Pikpak2DirectLink/server.env || (umask 077; printf "DATA_ENCRYPTION_KEY=%s\n" "$1" > /etc/Pikpak2DirectLink/server.env)' sh "$DATA_KEY"
unset DATA_KEY
```

```bash
sudo tee /etc/systemd/system/Pikpak2DirectLink.service > /dev/null <<EOF
[Unit]
Description=PikPak2DirectLink
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$(whoami)
WorkingDirectory=/opt/Pikpak2DirectLink
EnvironmentFile=/etc/Pikpak2DirectLink/server.env
Environment=ADDR=127.0.0.1:51873
# 反向代理与程序位于同一台服务器时保留 loopback 监听，并设置外部 HTTPS 地址。
#Environment=PUBLIC_BASE_URL=https://dl.example.com
# 可选：固定管理员访问密码。保持注释则首次打开网页时在页面中设置密码。
#Environment=ACCESS_PASSWORD=your_secure_password
ExecStart=/opt/Pikpak2DirectLink/Pikpak2DirectLink
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now Pikpak2DirectLink
sudo systemctl status Pikpak2DirectLink --no-pager
```

请单独备份 `/etc/Pikpak2DirectLink/server.env`，并确保它不会被普通用户、Web 服务或备份下载目录读取。

查看运行日志：

```bash
sudo journalctl -u Pikpak2DirectLink -f
```

重启或停止服务：

```bash
sudo systemctl restart Pikpak2DirectLink
sudo systemctl stop Pikpak2DirectLink
```

> 在线更新依赖 `Restart=always`。安装新版本后程序会替换当前二进制并正常退出，由 systemd 立刻拉起新版本。如果使用 `Restart=on-failure`，正常退出不会触发重启，更新后服务会停住。

## 使用方式

### 管理员后台

1. 新安装且未设置 `ACCESS_PASSWORD` 时，从启动日志取得包含 `#setup_token=...` 的完整初始设置 URL；远程管理服务器时先建立前述 SSH 隧道。
2. 通过服务器本机或 SSH 隧道打开该 URL 并设置管理员密码；如果配置了 `ACCESS_PASSWORD`，则直接使用该固定密码登录。
3. 初始化完成后再配置 HTTPS 反向代理和 `PUBLIC_BASE_URL`，公网部署通过 `https://dl.example.com` 访问。
4. 进入「账号」页面，添加一个或多个 PikPak 账号，并按需要调整每月流量额度。
5. 回到「解析」页面，粘贴磁力链接或 PikPak 分享链接；多行输入会自动创建批量任务。
6. 如分享链接需要提取码，填写提取码。
7. 选择「直链优先」或「代理优先」。
8. 等待任务完成，复制直链或代理链接，或推送到 aria2。
9. 如需排查问题，进入「日志」页面查看实时诊断信息。

### CDK 用户入口

管理员可在「CDK」页面生成兑换码，设置每个 CDK 的流量额度和有效期。用户访问：

```text
https://dl.example.com/u
```

输入 CDK 后即可使用解析功能。CDK 用户只能看到自己的任务、剩余流量和队列状态；失败信息会被收敛为用户可理解的提示，不暴露后台 PikPak 账号信息。

### aria2 推送

页面中的 aria2 配置保存在浏览器本地。aria2 需要开启 RPC，并允许浏览器跨域访问，例如：

```bash
aria2c --enable-rpc --rpc-listen-all --rpc-allow-origin-all
```

默认 RPC 地址为：

```text
http://localhost:6800/jsonrpc
```

如果 aria2 配置了 RPC token，可在页面的 aria2 配置弹窗中填写。

## 代理链接

代理链接由本程序服务端转发已获取到的 PikPak 下载直链，适合下载器无法直接使用原始直链、或希望固定入口地址的场景。

每个代理链接都会附带唯一令牌，例如：

```text
https://dl.example.com/proxy/<job-id>?token=<token>
```

代理链接不需要管理员登录即可访问，方便下载器直接调用；但只有持有令牌的人可以下载。解析完成后，程序会短期保留本次写入 PikPak 的临时文件，以便代理下载在直链临近过期或 CDN 早期断流时自动向 PikPak 刷新直链并重试。临时文件会在直链有效窗口结束后由后台自动清理；如果某个旧任务的临时文件已经被历史版本清理，代理链接仍可能需要重新解析后才能继续使用。

公网服务应只通过启用 TLS 的反向代理暴露。反向代理与应用位于同一台服务器时，应用应继续监听默认的 loopback HTTP 地址。将 `PUBLIC_BASE_URL` 设置为用户实际访问的 HTTPS 根地址：

```bash
PUBLIC_BASE_URL=https://your-domain.example
```

同机反向代理还应正确追加 `X-Forwarded-For`（例如 Nginx 的 `$proxy_add_x_forwarded_for`）。程序只在直接对端为 loopback 时信任该链，并取最右侧的非 loopback 地址用于登录限流；公网直连请求携带的同名头会被忽略。

该地址同时用于生成代理链接和决定会话 Cookie 的 `Secure` 属性。公网部署不要使用 `http://` 的 `PUBLIC_BASE_URL`，也不要绕过反向代理直接暴露应用端口。

## 配置项

除 `DATA_ENCRYPTION_KEY` 外，以下环境变量均为可选。未设置可选项时，程序会使用默认值或页面中的持久化设置。

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `DATA_ENCRYPTION_KEY` | 无，必填 | 标准 Base64 编码的 32 字节主密钥，用于 AES-256-GCM 字段加密。必须跨重启稳定保存。 |
| `DATA_ENCRYPTION_PREVIOUS_KEYS` | 空 | 轮换时用于读取旧数据的历史密钥，多个值以英文逗号分隔。 |
| `PIKPAK_USERNAME` | 空 | 可选的启动账号。通常建议在网页中添加账号。 |
| `PIKPAK_PASSWORD` | 空 | 与 `PIKPAK_USERNAME` 配套使用的启动账号密码。 |
| `ADDR` | `127.0.0.1:51873` | HTTP 服务监听地址。默认仅允许本机或 SSH 隧道访问；完成初始化后再按部署拓扑决定是否修改。 |
| `PUBLIC_BASE_URL` | 自动推断 | 生成代理链接时使用的公开访问地址。公网部署必须使用 HTTPS 反向代理并设置为 `https://...`。 |
| `ACCESS_PASSWORD` | 空 | 固定管理员访问密码。设置后跳过首次设置流程，登录页只接受此密码。 |
| `ACCESS_AUTH_FILE` | `data/auth.json` | 旧版管理员认证文件的迁移来源；v3.1.0 起活动数据保存在 SQLite。 |
| `PIKPAK_ROOT_FOLDER` | `Pikpak2DirectLink` | 程序在 PikPak 中创建临时目录时使用的根文件夹名。 |
| `PIKPAK_SESSION_FILE` | `data/pikpak-session.json` | 旧版启动账号 session 的迁移来源。 |
| `PIKPAK_ACCOUNTS_FILE` | `data/pikpak-accounts.json` | 旧版账号列表的迁移来源。 |
| `PIKPAK_ACCOUNT_SESSION_DIR` | `data/accounts` | 旧版多账号 session 的迁移来源。 |
| `DB_FILE` | `data/pikpak.db` | 唯一的活动持久化数据库，保存认证、账号、session、任务、CDK、设置和运维状态。 |
| `BACKUP_DIR` | `data/backups` | 已校验 SQLite 快照的保存目录。不要把加密密钥放在这里。 |
| `BACKUP_INTERVAL` | `24h` | 自动数据库备份间隔。 |
| `BACKUP_RETENTION` | `7` | 自动保留的成功数据库备份数量。 |
| `PIKPAK_REQUEST_TIMEOUT` | `20s` | 单次 PikPak API 请求超时时间。 |
| `RESOLVE_TIMEOUT` | `12m` | 单个账号处理一次资源解析的最长时间。 |
| `POLL_INTERVAL` | `5s` | 等待离线下载或转存完成时的轮询间隔。 |
| `RESOLVE_CONCURRENCY` | `1` | 首次启动时的解析并发数默认值；之后以「设置」页面保存到数据库的值为准。 |
| `QUEUE_TIMEOUT` | `60s` | 串行模式下新任务的默认任务超时预算。 |
| `PARALLEL_QUEUE_TIMEOUT` | `2m` | 并行模式下新任务的默认任务超时预算。 |
| `UPDATE_REPO` | `MengStar-L/Pikpak2DirectLink` | 在线更新检查的 GitHub 仓库，格式为 `owner/name`。 |
| `UPDATE_CHECK_INTERVAL` | `6h` | 后台自动检查更新的间隔。设为 `0` 可关闭后台检查，仍可手动检查。 |
| `ACCOUNT_HEALTH_CHECK_URL` | `https://mypikpak.com/s/VOveL7ZI01ViAz9VVKGgSWDlo2` | 账号可用性测试用的 PikPak 分享链接。 |
| `ACCOUNT_HEALTH_CHECK_INTERVAL` | `6h` | 每个账号两次凭据验证之间的间隔。 |
| `ACCOUNT_AUTO_REFRESH_GAP` | `30m` | 后台自动刷新不同账号登录凭据的最小间隔。 |
| `ACCOUNT_HEALTH_CHECK_TIMEOUT` | `60s` | 单次账号可用性测试的超时预算。 |

示例：

```bash
DATA_ENCRYPTION_KEY=<由 openssl rand -base64 32 生成并安全保存的值>
ADDR=127.0.0.1:51873
PUBLIC_BASE_URL=https://dl.example.com
ACCESS_PASSWORD=your_secure_password
RESOLVE_CONCURRENCY=2
UPDATE_CHECK_INTERVAL=6h
```

> 「设置」页面中保存的并发数和任务超时会写入 SQLite 数据库，重启后继续生效。`RESOLVE_CONCURRENCY`、`QUEUE_TIMEOUT` 和 `PARALLEL_QUEUE_TIMEOUT` 更适合作为首次启动时的初始值。

## 安全建议

- `DATA_ENCRYPTION_KEY` 必须由安全随机数生成并独立保管。不要把密钥写入仓库、镜像、公开日志或与数据库相同的可下载备份位置。
- 访问门始终开启。不设置 `ACCESS_PASSWORD` 时，只能持启动日志中的短期 token 从服务器本机或 SSH 隧道完成首次管理员密码设置；不要公开或经公网反向代理使用这条 URL。设置 `ACCESS_PASSWORD` 时，密码由环境变量固定，无法在网页中修改。
- 公网部署应在完成首次初始化后再启用可信 HTTPS 反向代理；务必使用强密码，并将 `PUBLIC_BASE_URL` 设置为外部 HTTPS 地址。
- PikPak 账号密码、session 和完整任务内容以 AES-256-GCM 字段加密后保存在 SQLite；索引、状态和审计元数据不是全库加密，请继续限制服务器、数据库和备份目录权限。
- 代理链接虽然带令牌，但仍应避免公开传播；拿到链接的人可以在有效期内下载对应文件。
- CDK 用户入口 `/u` 是公开页面，访问能力由 CDK 本身控制。请合理设置 CDK 流量额度和有效期。
- 升级前如需回滚点，必须先停止新任务并备份 `data/`；恢复旧快照会丢失快照之后产生的用户、额度、任务和设置变更。

## 在线更新

程序内置自更新能力，更新源为 GitHub Releases。

工作方式：

1. 仓库推送 `v*` 标签时，GitHub Actions 会构建对应平台的二进制，并发布 `SHA256SUMS`。
2. 程序按 `UPDATE_CHECK_INTERVAL` 定时检查最新 Release，并在「更新」入口显示可用更新。
3. 管理员可在「更新」页面手动检查，也可在发现新版本后点击「立即更新」。
4. 更新器会下载与当前 `os/arch` 匹配的二进制，校验 `SHA256SUMS`，替换当前可执行文件，然后触发优雅停机并等待 systemd 重启。

发布文件命名约定：

```text
Pikpak2DirectLink_linux_amd64
Pikpak2DirectLink_linux_arm64
Pikpak2DirectLink_darwin_amd64
Pikpak2DirectLink_darwin_arm64
Pikpak2DirectLink_windows_amd64.exe
SHA256SUMS
```

注意事项：

- 服务进程必须对可执行文件所在目录有写权限。
- systemd 服务请使用 `Restart=always`。
- 管理员和用户会话保存在 SQLite；正常更新重启后仍可继续使用，过期或主动注销的会话除外。
- 如果本地构建版本显示为 `dev`，更新器会把它视为早于任何正式 Release。

## 数据存储与恢复

### SQLite 与密钥

v3.1.0 起，`data/pikpak.db` 是服务端唯一的活动持久化数据源。管理员和用户会话只保存令牌摘要；PikPak 密码、session、应用 Secret 和完整任务内容使用 `DATA_ENCRYPTION_KEY` 做 AES-256-GCM 字段加密。任务完整详情保留 3 小时，清除敏感详情后的摘要最多保留 30 天。

这不是全库加密：账号标识、任务状态、时间、计费量等查询元数据仍可能以明文存在。请同时保护数据库文件、备份目录和服务器访问权限。

生成新密钥时使用密码学安全随机数，不要使用密码、UUID 或重复值。Linux/macOS 可运行：

```bash
openssl rand -base64 32
```

PowerShell 可运行：

```powershell
$key = New-Object byte[] 32
$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
$rng.GetBytes($key)
[Convert]::ToBase64String($key)
$rng.Dispose()
```

把输出的单行 Base64 值安全写入服务环境，之后不要重新生成覆盖。

### 从 v3.0.x 首次升级

1. 停止提交新任务，等待旧版本内存中的运行和排队任务全部结束；这些任务不在旧版磁盘数据中，无法迁移或恢复。
2. 停止服务并额外备份整个 `data/` 目录。
3. 生成并永久保存 `DATA_ENCRYPTION_KEY`，安装 v3.1.0 后执行一次维护启动。
4. 程序会先把旧数据库、`auth.json`、账号 JSON 和 session 文件复制到带校验清单的 `data/migration-backups/pending/`，再自动导入并验证 SQLite 数据。
5. 登录后台确认账号、用户、CDK 和设置正常。在「设置 → 数据库备份」中勾选确认后，删除可能仍含明文凭据的迁移备份。

迁移过程可重复启动，不会自动恢复或重新执行未完成任务。服务重启时仍未结束的持久化任务会标记为 `failed/service_restart`。

### 密钥轮换

1. 生成新的 32 字节 Base64 密钥并设为 `DATA_ENCRYPTION_KEY`。
2. 把旧主密钥加入 `DATA_ENCRYPTION_PREVIOUS_KEYS`；有多个旧密钥时以英文逗号分隔，并重启服务。
3. 确认启动和账号访问正常。只要仍需读取旧数据库快照或尚未过期的旧记录，就继续安全保留历史密钥。

不要先移除旧密钥再重启，否则仍由旧密钥加密的数据无法读取。数据库快照不包含密钥，恢复某个旧快照时必须同时具备该快照对应的密钥。

### 自动备份

程序默认每 24 小时在 `data/backups/` 创建 SQLite 快照，发布前执行完整性检查和 SHA-256 计算，仅保留最近 7 份成功快照。管理员也可在「设置 → 数据库备份」中手动创建并查看最近状态。可用 `BACKUP_DIR`、`BACKUP_INTERVAL` 和 `BACKUP_RETENTION` 修改默认值。

备份目录仍应限制为服务用户可读，并应复制到独立存储。数据库备份本身不能替代加密密钥备份。

### 离线恢复

恢复前停止服务，确认没有其它进程打开数据库，然后使用与正常运行相同的路径配置。两个命令都必须显式传入 `--yes`；恢复工具会先为当前数据创建安全副本。

恢复一个已校验的 SQLite 快照：

```bash
./Pikpak2DirectLink storage restore-db --backup data/backups/<backup>.db --yes
```

恢复首次升级前的迁移快照：

```bash
./Pikpak2DirectLink storage restore-migration --backup data/migration-backups/pending --yes
```

恢复后再启动服务并检查日志。`restore-db` 会把系统恢复到数据库快照时刻；`restore-migration` 会回到升级前的旧文件布局。两种恢复都会丢失备份创建之后的用户、额度消费、账号状态、任务和配置变更。不要让旧版二进制直接打开 v3.1.0 数据库；需要回滚旧版本时，应停止服务并使用迁移快照恢复。

## 说明

PikPak 没有稳定公开的官方开发接口文档。本程序依赖非官方接口调用方式；如果 PikPak 调整登录、验证码、离线下载、分享转存或下载链接接口，程序可能需要相应更新。

本项目仅用于学习、研究和个人合法使用。请遵守所在地区法律法规、平台服务条款和资源版权要求。
