# PikPak2DirectLink

一个用 Go 写的服务端小工具，提供网页页面，让用户输入磁链或 PikPak 分享链接，借助 PikPak 会员的转存/离线下载能力拿到下载直链，或者走服务端代理中转。

## 现状

- 已包含 Go 后端、任务轮询、网页界面、代理下载接口
- 支持两类输入：
  - `magnet:?xt=...`
  - `https://mypikpak.com/s/...` 或 `https://mypikpak.com/s/...`
- 分享链接如果包含多个文件，会先要求选择
- 磁链或转存结果如果包含多个文件，也会要求选择最终要生成链接的文件
- 支持添加多个 PikPak 账号，解析时会按账号顺序自动重试
- 会把 PikPak 账号、密码和会话缓存到本地，session 失效时自动重新登录

## 注意

PikPak 并没有稳定公开的官方开发接口文档，这个项目目前依赖的是社区常见的非官方接口调用方式。后续如果 PikPak 调整了登录、验证码或下载接口，这个服务也可能需要跟着改。

## 环境变量

默认不再强制要求环境变量。你可以直接启动服务，然后在网页的「账号管理」页面添加 PikPak 账号。服务端会把账号、密码、refresh token 和必要会话信息保存到本地。下次启动时自动恢复；如果 session 过期或失效，会尝试用保存的账号密码重新登录。

如果你仍然想用环境变量作为启动时的备用账号，也可以设置：

```bash
PIKPAK_USERNAME=your_account
PIKPAK_PASSWORD=your_password
```

其他可选项：

```bash
ADDR=:8080
PUBLIC_BASE_URL=http://localhost:8080
PIKPAK_ROOT_FOLDER=Pikpak2DirectLink
PIKPAK_SESSION_FILE=data/pikpak-session.json
PIKPAK_ACCOUNTS_FILE=data/pikpak-accounts.json
PIKPAK_ACCOUNT_SESSION_DIR=data/accounts
PIKPAK_REQUEST_TIMEOUT=20s
RESOLVE_TIMEOUT=12m
POLL_INTERVAL=5s
```

## 运行

```bash
go run ./cmd/server
```

打开：

```text
http://localhost:8080
```

第一次打开时如果还没有本地 session，先在网页端登录一次。登录成功后就会自动持久化保存 refresh token。

现在账号管理是单独页面。添加多个账号后，解析任务会从第一个账号开始尝试；如果当前账号调用失败，会标记该账号失败并自动切换到下一个账号，直到有账号成功或者全部账号失败。失败标记可以在账号管理页面手动重置。

## 代理下载

任务完成后页面会同时给出：

- PikPak 直链
- 服务端代理链接 `/proxy/{job_id}`

代理模式下，请求会由你的服务端去拉取 PikPak 下载地址，再把文件流返回给客户端。

## 开发

```bash
go test ./...
```
