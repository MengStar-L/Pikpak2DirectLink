# PikPak2DirectLink

PikPak2DirectLink 是一个基于 Go 的网页工具，用于将磁力链接或 PikPak 分享链接解析为可下载链接。程序通过 PikPak 账号的离线下载、转存和文件下载能力获取直链，并提供服务端代理下载入口。

## 功能

- 支持解析 `magnet:?xt=...` 磁力链接。
- 支持解析 PikPak 分享链接。
- 支持多个 PikPak 账号。
- 当前账号解析失败时，会自动切换到下一个账号继续尝试。
- 账号 session 失效时，会使用已保存的账号密码重新登录。
- 解析失败的账号会被标记，可在账号管理页面重置。
- 分享链接或解析结果包含多个文件时，可在页面中选择目标文件。
- 解析完成后提供 PikPak 直链和服务端代理链接。

## 安装

默认安装目录为 `/opt/Pikpak2DirectLink`。

服务器需要已安装 Git 和 Go。安装命令如下：

```bash
sudo mkdir -p /opt/Pikpak2DirectLink
sudo chown -R "$USER:$USER" /opt/Pikpak2DirectLink

git clone https://github.com/MengStar-L/Pikpak2DirectLink.git /opt/Pikpak2DirectLink
cd /opt/Pikpak2DirectLink

go build -o Pikpak2DirectLink ./cmd/server
```

## 运行

在安装目录执行：

```bash
cd /opt/Pikpak2DirectLink
./Pikpak2DirectLink
```

默认监听地址：

```text
http://localhost:51873
```

默认端口为 `51873`。如需修改端口，可通过环境变量 `ADDR` 和 `PUBLIC_BASE_URL` 指定。

## 使用

1. 打开网页。
2. 进入账号管理页面。
3. 添加一个或多个 PikPak 账号。
4. 返回解析页面。
5. 输入磁力链接或 PikPak 分享链接。
6. 如分享链接需要提取码，填写提取码。
7. 选择直链或代理方式。
8. 提交解析任务。
9. 等待任务完成后复制下载链接。

## 配置项

以下环境变量均为可选：

```bash
ADDR=:51873
PUBLIC_BASE_URL=http://localhost:51873
PIKPAK_ROOT_FOLDER=Pikpak2DirectLink
PIKPAK_ACCOUNTS_FILE=data/pikpak-accounts.json
PIKPAK_ACCOUNT_SESSION_DIR=data/accounts
PIKPAK_REQUEST_TIMEOUT=20s
RESOLVE_TIMEOUT=12m
POLL_INTERVAL=5s
```

也可以通过环境变量预置一个启动账号：

```bash
PIKPAK_USERNAME=your_account
PIKPAK_PASSWORD=your_password
```

## 数据保存

程序会在本地保存 PikPak 账号、密码和 session 信息。默认保存位置为：

```text
data/pikpak-accounts.json
data/accounts/
```

请妥善保护服务器和数据目录权限。

## 说明

PikPak 没有稳定公开的官方开发接口文档。本程序依赖非官方接口调用方式。若 PikPak 调整登录、验证码、离线下载或下载链接接口，程序可能需要相应更新。
