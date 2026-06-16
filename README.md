# PikPak2DirectLink

PikPak2DirectLink 是一个基于 Go 的网页工具，用于将磁力链接或 PikPak 分享链接解析为可下载链接。程序通过 PikPak 账号的离线下载、转存和文件下载能力获取直链，并提供服务端代理下载入口。

## 功能

- 支持解析 `magnet:?xt=...` 磁力链接。
- 支持解析 PikPak 分享链接。
- 支持多个 PikPak 账号。
- 当前账号解析失败时，会自动切换到下一个账号继续尝试。
- 账号 session 失效时，会使用已保存的账号密码重新登录。
- 账号管理页面会显示账号是否为会员，以及会员到期时间。
- 解析失败的账号会被标记，可在账号管理页面重置。
- 分享链接或解析结果包含多个文件时，可在页面中选择目标文件。
- 解析完成后提供 PikPak 直链和服务端代理链接。
- 解析完成后会清理本次转存或离线下载产生的 PikPak 临时文件。
- 日志页面提供实时控制台诊断，可查看账号尝试、文件检测、直链获取、临时文件清理等过程。
- **可选访问密码保护**：设置 `ACCESS_PASSWORD` 环境变量后，UI 和 API 需要登录才能访问。
- **代理链接令牌保护**：每个代理链接附带唯一令牌，防止未授权访问。
- **深色模式支持**：自动跟随系统主题，或手动切换浅色/深色模式。
- **实时链接检测**：输入时自动识别磁力链接或 PikPak 分享链接。
- **任务内存限制**：自动清理旧任务（保留最近 200 个），防止内存泄漏。
- **在线更新**：在「更新」页面检测 GitHub 上对应架构（os/arch）的最新版本，一键下载并安装；下载与安装过程显示进度条，安装后服务自动重启。后台还会定时检查新版本并在侧边栏提示。

## 安装

默认安装目录为 `/opt/Pikpak2DirectLink`。本程序面向 Linux 服务器运行。

推荐直接下载预编译二进制（方式一）；如需自行从源码构建，见方式二。

### 方式一：下载预编译二进制（推荐）

[Releases](https://github.com/MengStar-L/Pikpak2DirectLink/releases) 提供各架构的预编译二进制，文件命名为 `Pikpak2DirectLink_<os>_<arch>`（如 `Pikpak2DirectLink_linux_amd64`、`Pikpak2DirectLink_linux_arm64`）。下载后即可运行，无需安装 Go 环境。

以 Linux x86_64 为例：

```bash
sudo apt update
sudo apt install -y curl ca-certificates

sudo mkdir -p /opt/Pikpak2DirectLink
sudo chown -R "$USER:$USER" /opt/Pikpak2DirectLink
cd /opt/Pikpak2DirectLink

# 下载对应架构的最新版本二进制（此处为 linux_amd64；arm64 机器改为 Pikpak2DirectLink_linux_arm64）
curl -L -o Pikpak2DirectLink \
  https://github.com/MengStar-L/Pikpak2DirectLink/releases/latest/download/Pikpak2DirectLink_linux_amd64
chmod +x Pikpak2DirectLink
```

> 不确定架构时，用 `uname -m` 查看：`x86_64` 对应 `amd64`，`aarch64` 对应 `arm64`。
>
> 预编译二进制已写入版本号，「更新」页面会据此与 GitHub 最新 Release 比较，并直接下载替换二进制完成更新——无需在服务器上保留 Go 环境。

### 方式二：从源码构建

需要本机安装 Go。以 Debian/Ubuntu x86_64 为例：

```bash
sudo apt update
sudo apt install -y git curl ca-certificates

curl -LO https://go.dev/dl/go1.26.2.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.2.linux-amd64.tar.gz
rm -f go1.26.2.linux-amd64.tar.gz

sudo mkdir -p /opt/Pikpak2DirectLink
sudo chown -R "$USER:$USER" /opt/Pikpak2DirectLink

git clone https://github.com/MengStar-L/Pikpak2DirectLink.git /opt/Pikpak2DirectLink
cd /opt/Pikpak2DirectLink

/usr/local/go/bin/go build -ldflags "-X pikpak2directlink/internal/version.Version=$(git describe --tags --always)" -o Pikpak2DirectLink ./cmd/server
```

> `-ldflags "-X .../version.Version=..."` 会把版本号写入二进制，「更新」页面据此与 GitHub 最新 Release 比较。不带该参数构建时版本显示为 `dev`，仍可正常使用与更新。

## 运行

直接运行：

```bash
cd /opt/Pikpak2DirectLink
./Pikpak2DirectLink
```

默认监听地址：

```text
http://your-server-ip:51873
```

默认端口为 `51873`。如需修改端口，可通过环境变量 `ADDR` 指定。

## 自启动

使用 `systemd` 配置开机自启动：

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
Environment=ADDR=:51873
Environment=ACCESS_PASSWORD=your_secure_password
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

> 在线更新依赖 `Restart=always`：安装新版本后程序会替换二进制并正常退出，由 systemd 立即重启到新版本。若使用 `Restart=on-failure`，正常退出（退出码 0）不会被重启，更新后服务会停止。

如需固定代理链接中的公开访问地址，可在服务文件的 `[Service]` 段增加：

```ini
Environment=PUBLIC_BASE_URL=http://your-server-ip:51873
```

查看运行日志：

```bash
sudo journalctl -u Pikpak2DirectLink -f
```

重启或停止服务：

```bash
sudo systemctl restart Pikpak2DirectLink
sudo systemctl stop Pikpak2DirectLink
```

## 使用

1. 打开网页。
2. （可选）如果配置了访问密码，输入密码登录。
3. 进入账号管理页面。
4. 添加一个或多个 PikPak 账号。
5. 返回解析页面。
6. 输入磁力链接或 PikPak 分享链接（会自动识别链接类型）。
7. 如分享链接需要提取码，填写提取码。
8. 选择直链或代理方式。
9. 提交解析任务。
10. 等待任务完成后复制下载链接。
11. 如需排查解析过程，可进入日志页面查看实时诊断信息。
12. 右下角可切换深色/浅色主题。

## 代理链接

代理链接由本程序服务端转发已获取到的 PikPak 下载直链，适合下载器无法直接使用原始直链的场景。

每个代理链接包含唯一令牌，仅持有令牌者可访问。代理链接不需要登录即可使用，方便下载器直接调用。

解析完成后，程序会删除本次写入 PikPak 的临时文件，因此代理链接不会再重新向 PikPak 刷新直链。代理链接的有效期受已获取直链的过期时间影响，过期后需要重新解析。

## 安全建议

- 如果服务暴露在公网，**强烈建议**设置 `ACCESS_PASSWORD` 环境变量。未设置时，任何人都可以添加/删除账号、查看日志。
- 妥善保护服务器和数据目录权限，账号密码和 session 信息以明文保存在本地。
- 代理链接包含令牌保护，但仍应避免公开分享。

## 配置项

以下环境变量均为可选：

```bash
ADDR=:51873
PUBLIC_BASE_URL=http://your-server-ip:51873
ACCESS_PASSWORD=your_secure_password
PIKPAK_ROOT_FOLDER=Pikpak2DirectLink
PIKPAK_ACCOUNTS_FILE=data/pikpak-accounts.json
PIKPAK_ACCOUNT_SESSION_DIR=data/accounts
PIKPAK_REQUEST_TIMEOUT=20s
RESOLVE_TIMEOUT=12m
POLL_INTERVAL=5s
UPDATE_REPO=MengStar-L/Pikpak2DirectLink
UPDATE_CHECK_INTERVAL=6h
```

**重要配置项说明：**

- `ACCESS_PASSWORD`：访问密码。设置后，UI 和 API 需要登录才能访问。**强烈建议在公网部署时设置。**
- `PUBLIC_BASE_URL`：代理链接的公开访问地址。如果服务通过反向代理或域名访问，应设置为实际访问地址。
- `UPDATE_REPO`：检测更新时使用的 GitHub 仓库（`owner/name`）。默认指向官方仓库。
- `UPDATE_CHECK_INTERVAL`：后台检查新版本的间隔。设为 `0` 可关闭后台自动检查（仍可在「更新」页面手动检查）。

也可以通过环境变量预置一个启动账号：

```bash
PIKPAK_USERNAME=your_account
PIKPAK_PASSWORD=your_password
```

## 在线更新

程序内置自更新能力，更新源为 GitHub Releases。

工作方式：

1. 仓库推送 `v*` 形式的标签（如 `v1.2.0`）时，`.github/workflows/release.yml` 会交叉编译各架构二进制（linux/darwin/windows × amd64/arm64），并连同 `SHA256SUMS` 一起发布到对应 Release。
2. 程序在后台按 `UPDATE_CHECK_INTERVAL` 周期检查最新 Release，发现新版本时在侧边栏「更新」按钮上显示红点。
3. 在「更新」页面可手动「检查更新」，或在发现新版本时点击「立即更新」。
4. 更新时程序下载与当前 `os/arch` 匹配的二进制（显示下载进度条），校验 `SHA256SUMS`，替换正在运行的可执行文件，然后退出由 systemd 重启到新版本。

发布二进制的命名约定为 `Pikpak2DirectLink_<os>_<arch>`（Windows 追加 `.exe`），更新器据此选择对应架构的资源。

注意：

- 在线更新需要服务进程对自身可执行文件所在目录有写权限（按本文「安装」步骤将 `/opt/Pikpak2DirectLink` 归属当前用户即满足）。
- 更新依赖 systemd 自动重启，请确保服务文件使用 `Restart=always`（见「自启动」）。
- 更新后内存中的登录会话会失效，需要重新输入访问密码登录。

## 数据保存

程序会在本地保存 PikPak 账号、密码和 session 信息。默认保存位置为：

```text
data/pikpak-accounts.json
data/accounts/
```

请妥善保护服务器和数据目录权限。

## 说明

PikPak 没有稳定公开的官方开发接口文档。本程序依赖非官方接口调用方式。若 PikPak 调整登录、验证码、离线下载或下载链接接口，程序可能需要相应更新。
