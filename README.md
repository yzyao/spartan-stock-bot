# SpartanStockBot

Spartan Host VPS 库存 Telegram 通知服务。Go + chromedp + 无头 Chromium + goquery，无数据库或网页界面。默认启用无头浏览器，面向 Debian VPS，通过 Docker Compose 持续运行；支持 `FETCH_MODE=http` 普通 HTTP 和 `FETCH_MODE=tls` 浏览器 TLS/HTTP2 指纹模式。

推荐采用 **公开频道接收通知 + 管理员私聊 bot**：订阅者无需逐个启动机器人，管理员仍独立控制检查。购买按钮指向对应套餐的 Spartan 官方订单页面，自动追加 `aff=1194`，不显示长 URL、不使用第三方短链。推广 ID 可配置；是否计入佣金由 Spartan 的推广系统决定。频道广告和商业合作由 Telegram/频道管理，不由此程序自动开通。

若 HTTP、TLS 和浏览器均被 Cloudflare 拦截，可先运行 [两个 GitHub 抓取方案的独立对照实验](experiments/cloudflare/README.md)：`bash experiments/cloudflare/run.sh both`。实验不改生产抓取模式、不读写库存状态、不发送 Telegram，默认只请求一个分类；确认拿到真实商品 HTML 后再考虑接入。

## 功能

- 默认监控 Seattle 的 CMIN2、Premium KVM、E5 KVM 三个分类，默认每次检查完成后等待 60 秒。
- 一个常驻 Chromium 串行访问页面，运行 JavaScript、在进程内共享 Cookie；默认每个进程使用独立临时 profile，每页使用临时标签页并在结束后关闭，浏览器崩溃后自动重建。
- TLS 模式使用固定版本的 Go tls-client，无需启动 Chromium；支持 Chrome 150 / Firefox 148 指纹、请求头顺序、内存 Cookie、HTTPS 证书校验和同站重定向。它不执行 JavaScript、不自动解决验证码，不保证 Cloudflare 放行。
- 初次发现有货发送新消息；同轮库存增减、价格等资料变化只编辑原消息；售罄编辑历史消息并关闭该轮；再次有货发送新消息。
- 失败页面、缺失商品或未知库存保留旧值，绝不推断为零库存。
- 启动前加载 JSON 状态，原子替换保存消息 ID；临时 Telegram 错误保留活动消息并重试。
- `/status`、`/check`、`/help`、`/start`；仅允许 `.env` 中配置的管理员在私聊中使用。其他消息被忽略。
- 定时/手动检查互斥、结构化 JSON 日志、SIGINT/SIGTERM 取消、非 root 容器、日志轮转。

## 环境与目录

本地开发使用 Go 1.27（开发验证版本 1.27.3），浏览器模式需安装 Chrome/Chromium。Docker 镜像自带 Chromium，Debian 宿主机只需 Docker Engine 与 Docker Compose 插件、可写磁盘和访问 Spartan/Telegram 的 HTTPS 网络。建议为浏览器预留内存，优先使用至少 1 GB 内存的 VPS 并实际观察峰值；HTTP 模式资源开销更小。无需开放入站端口。一个状态文件和浏览器 profile 只允许一个实例使用，**不要多副本部署**。

```text
cmd/spartan-stock-bot/   入口、命令处理、优雅退出、只读验证模式
internal/config/        环境配置与校验
internal/model/         商品与持久状态
internal/spartan/       Chromium/HTTP/TLS 抓取、浏览器生命周期与隔离的 HTML 解析器
internal/state/         JSON 读取、损坏备份、原子写入
internal/monitor/       提供商接口、库存状态机与调度
internal/telegram/      Telegram API、长轮询、鉴权与消息格式
testdata/               离线 HTML 夹具及来源说明
data/                   持久状态挂载目录
Dockerfile
docker-compose.yml
.env.example
```

## 创建 Telegram Bot、配置频道和管理员

1. 在 Telegram 打开官方 [@BotFather](https://t.me/BotFather)，发送 `/newbot`。名称可用 `SpartanStockBot`，用户名必须以 `bot` 结尾且未被占用。保存 BotFather 返回的 token。
2. 可在 BotFather 的 `/setcommands` 配置：

   ```text
   status - 查看库存
   check - 立即检查
   help - 帮助
   ```

3. 每位管理员与 bot **私聊**发送 `/start`。机器人只接受配置的用户 ID，不通过用户名鉴权，不向普通订阅者开放命令，也没有聊天命令修改配置的入口。
4. 创建公开 Telegram 频道，把 bot 加为管理员并授予**发布消息、编辑消息**权限。库存通知发布到频道，命令结果只回复提出请求的管理员私聊。公众加入频道即可订阅。
5. 获取管理员 ID/频道 ID 时，先确保 bot 没在运行。在自己电脑浏览器临时访问 `https://api.telegram.org/bot<TOKEN>/getUpdates`，其中 `<TOKEN>` 替换成自己的 token。管理员私聊的 `message.from.id` 是 `TELEGRAM_ADMIN_IDS`，`message.chat.id` 是 `TELEGRAM_CHAT_ID`。频道里手动发一条测试文字，响应中的 `channel_post.chat.id` 是频道数字 ID，通常形如 `-100...`。不要把含 token 的地址或完整响应发到公开频道。
6. 如果拿不到频道更新，移除再添加 bot 管理员后，在频道发一条新消息；也可调用 `getChat` 并以公开频道的 `@username` 为 `chat_id` 获取数字 `id`。运行本程序前关闭临时 `getUpdates` 请求，避免多个长轮询消费者冲突。以前配置过 webhook 时先调用 Bot API `deleteWebhook`，本程序使用长轮询。

Telegram 官方的频道运营与变现说明见 [频道广告分成](https://telegram.org/blog/monetization-for-channels) 和 [建议帖子](https://telegram.org/blog/checklists-suggested-posts)。具体资格、地区和收益以 Telegram 账户实际显示为准。

## 配置

```bash
cp .env.example .env
nano .env
```

至少填写 token、你自己的私聊 ID、管理员用户 ID；多人订阅请同时填写频道 ID：

```dotenv
TELEGRAM_BOT_TOKEN=填写BotFather提供的token
TELEGRAM_CHAT_ID=填写自己的私聊数字ID
TELEGRAM_ADMIN_IDS=填写管理员用户ID，多个用英文逗号分隔
TELEGRAM_CHANNEL_ID=填写频道的负数数字ID
```

上述中文是占位说明，部署时必须替换为真实值。不要提交 `.env`。

| 配置 | 默认值 / 用途 |
| --- | --- |
| `TELEGRAM_BOT_TOKEN` | 必填，机器人 token |
| `TELEGRAM_CHAT_ID` | 必填，管理员私聊 ID；未设频道时也作为通知目标 |
| `TELEGRAM_ADMIN_IDS` | 允许的用户 ID，逗号分隔；留空时使用 `TELEGRAM_CHAT_ID` 作为唯一管理员 |
| `TELEGRAM_CHANNEL_ID` | 频道数字 ID，如 `-100...`；留空为个人通知模式 |
| `CHECK_INTERVAL_SECONDS` | `60`，1–86400 秒；建议保持至少 60 秒，避免频繁请求 |
| `HTTP_TIMEOUT_SECONDS` | `15`，3–300 秒；用于 HTTP/TLS 请求，TLS 模式包含重定向和响应体读取；长轮询服务端等待比客户端超时短 |
| `FETCH_MODE` | `browser`，默认直接启用无头 Chromium；可设为 `http` 或 `tls`，不自动切换模式 |
| `TLS_CLIENT_PROFILE` | `chrome_150`；可选 `firefox_148`，仅用于 TLS 模式，与 User-Agent 配套，不接受任意名称 |
| `TLS_PROXY_URL` | 可选 HTTP/HTTPS/SOCKS5 代理，仅用于 TLS 模式；不支持 URL 用户名密码，不自动采用 `HTTP_PROXY`/`HTTPS_PROXY` |
| `BROWSER_TIMEOUT_SECONDS` | `45`，5–300 秒；启动及单页加载各自的超时，超时取消不会变成零库存 |
| `BROWSER_EXECUTABLE` | 本地自动寻找 Chrome/Chromium；Docker 内为 `/usr/bin/chromium` |
| `BROWSER_PROFILE_DIR` | 默认留空，为每个浏览器进程创建独立临时 profile；可显式设置 `/data/browser` 保存 Cookie，但必须自行管理单实例及旧锁 |
| `BROWSER_PROXY_URL` | 可选 HTTP/HTTPS/SOCKS5 代理 URL，不支持 URL 中的用户名密码；浏览器不自动采用 `HTTP_PROXY`/`HTTPS_PROXY` |
| `BROWSER_NO_SANDBOX` | 本地默认 `false`；Docker 镜像设置为 `true`，见下文容器隔离说明 |
| `STATE_FILE` | `/data/state.json` |
| `LOG_LEVEL` | `info`，支持 `debug/info/warn/error` |
| `TZ` | `Asia/Shanghai`；持久化时间使用 UTC，消息按此时区显示 |
| `SPARTAN_AFFILIATE_ID` | `1194`，购买按钮自动附加的 `aff` 参数 |
| `SPARTAN_URLS` | 可选，逗号分隔的 HTTPS 分类 URL，覆盖默认三个地址 |

环境变量由进程或 Compose 提供，Go 程序**不会自动读取 `.env` 文件**。修改配置后重建/重建容器。管理员列表不会改变频道的订阅权限。

## Docker Compose 部署（Debian）

在仓库目录执行：

```bash
git clone https://github.com/yzyao/spartan-stock-bot.git
cd spartan-stock-bot
cp .env.example .env
nano .env
mkdir -p data
sudo chown -R 65532:65532 data
sudo chmod 700 data
docker compose up -d --build
```

运行镜像使用 Debian slim + Chromium（UID/GID 65532），自带 HTTPS 根证书；时区数据库嵌入 Go 二进制。宿主机挂载目录必须允许该 UID 写入。Compose 挂载 `./data:/data`，根文件系统只读，提供 128 MB `/tmp` tmpfs、256 MB `/dev/shm`，并用 `init: true` 回收浏览器子进程。不映射网络端口，Chrome DevTools 使用本地管道，不开启调试监听端口。浏览器子进程只接收必要的操作系统环境变量，不继承 Telegram token。

Docker 中为兼容容器内核/namespace 限制，镜像设置 `BROWSER_NO_SANDBOX=true`，关闭 Chromium 内部沙箱；仍使用非 root、移除全部 capabilities、`no-new-privileges` 和容器隔离。只访问配置的库存页面，不使用个人浏览器 profile。支持 Chromium 用户命名空间沙箱的部署可显式设 `BROWSER_NO_SANDBOX=false` 并验证启动。

验证解析器前先停止正式服务。验证不需要 Telegram token，不读写 `state.json`、不发消息；默认使用独立临时 profile。若显式设置了持久 `BROWSER_PROFILE_DIR`，必须确保没有其他 Chromium 正在使用它：

```bash
docker compose stop
docker compose run --rm --no-deps spartan-stock-bot -verify
```

在同一台 VPS 分别验证三种模式（命令只覆盖本次运行，不修改 `.env`）：

```bash
docker compose build
docker compose stop

# 普通 HTTP
docker compose run --rm --no-deps -e FETCH_MODE=http spartan-stock-bot -verify

# Go TLS/HTTP2 Chrome 指纹，不启动 Chromium
docker compose run --rm --no-deps -e FETCH_MODE=tls -e TLS_CLIENT_PROFILE=chrome_150 spartan-stock-bot -verify

# 可选：对照 Firefox 指纹
docker compose run --rm --no-deps -e FETCH_MODE=tls -e TLS_CLIENT_PROFILE=firefox_148 spartan-stock-bot -verify

# 无头 Chromium
docker compose run --rm --no-deps -e FETCH_MODE=browser spartan-stock-bot -verify
```

每次命令后可用 `echo $?` 查看退出码。成功应为 `0`、`categories: 3`、非空 `products` 且无 `issues`；失败返回非零，并输出每个页面的问题。浏览器默认每页最多等待 45 秒，三页都遇到持续挑战时总耗时可能超过两分钟。验证完成后根据成功的模式修改 `.env` 中 `FETCH_MODE`，再运行 `docker compose up -d --build`。

也可以在首次启动前先 `docker compose build`，再运行验证命令，最后 `docker compose up -d`。验证应输出 3 个成功分类、商品、数字库存、价格、月付周期和完整订单 URL，且退出码为 0。浏览器模式会等待普通 JavaScript/挑战页跳转直到超时，但无法保证 Cloudflare 放行，也不会点击或自动解决交互式验证码。

TLS 模式基于 [tls-client v1.17.0](https://github.com/bogdanfinn/tls-client/tree/v1.17.0)，用浏览器 ClientHello、HTTP/2 设置及配套请求头减小客户端指纹差异，不需要额外容器。为了兼容 VPS/代理，当前固定使用 HTTP/2 或服务端协商的 HTTP/1.1，不启用 HTTP/3。Cookie 只在本进程内复用，退出即丢弃；Telegram API 继续使用标准 `net/http`。TLS 证书校验始终开启，不接受降级 HTTP 或跨站重定向。

如果返回 `Cloudflare challenge` 或 `TLS HTTP 403`，仍表示验证未通过；TLS 指纹不等于执行 JavaScript 或完成人机验证。不要把它当作已经绕过 Cloudflare。确认 `-verify` 成功后，才在 `.env` 设置 `FETCH_MODE=tls` 和所用的 `TLS_CLIENT_PROFILE`，然后 `docker compose up -d --build`。默认 `FETCH_MODE=browser` 保持不变。

查看日志、停止：

```bash
docker compose logs -f
docker compose down
```

`down` 不删除宿主机 `data`。Debian 必须设置 Docker 开机启动；`restart: unless-stopped` 会在 Docker/主机重启后恢复服务，但手动停止的容器保持停止。

升级（先从你的代码来源更新文件，使用 Git 时可运行 `git pull --ff-only`）：

```bash
docker compose stop
sudo cp -a data "data.backup-$(date +%Y%m%d-%H%M%S)"
git pull --ff-only
docker compose up -d --build
docker compose logs --tail=100
```

保留 `.env` 和 `data`；备份应放在安全位置。切换发布频道时，已经活动的通知仍使用状态中的原 `chatId` 完成本轮编辑，后续新补货会发到新频道。若希望立即迁移，先停止服务、备份并移走旧状态，再启动；这会重新发送当前有货通知，旧消息不再跟踪。

## 本地开发与检查

```bash
go mod download
gofmt -w cmd internal
go test ./...
go vet ./...
go build ./...
mkdir -p build
go build -o build/spartan-stock-bot ./cmd/spartan-stock-bot
```

Linux/macOS 本地运行（自己创建并信任的 `.env`，值不含 shell 特殊字符；不要 source 外来的文件）：

```bash
set -a
. ./.env
set +a
export STATE_FILE="$PWD/data/state.json"
./build/spartan-stock-bot
```

本地 Debian 可先 `sudo apt-get update && sudo apt-get install -y chromium`。Windows PowerShell 可逐项设置 `$env:TELEGRAM_BOT_TOKEN`、`$env:TELEGRAM_CHAT_ID`、`$env:TELEGRAM_ADMIN_IDS`、`$env:TELEGRAM_CHANNEL_ID` 和 `$env:STATE_FILE="$PWD/data/state.json"`，按需设置 `$env:BROWSER_EXECUTABLE` 为 Chrome 路径，随后 `go run ./cmd/spartan-stock-bot`。本地默认保留 Chromium 内部沙箱。

纯站点验证不需要 Telegram 配置；指定持久 profile 时先确保它没被其他实例占用：

```bash
go run ./cmd/spartan-stock-bot -verify
```

不通过时退出码非 0，不能把验证错误当成零库存。单元测试只使用本地夹具、HTTP 测试服务器和假 Telegram，不访问真实站点、不向 Telegram 发测试消息。

安装 Chrome/Chromium 后可启用本地浏览器集成测试（仅访问本机测试服务器，不访问真实 Spartan/Telegram）：

```bash
SPARTAN_BROWSER_TESTS=1 go test ./internal/spartan -run TestBrowserIntegration -v
```

可设置 `BROWSER_EXECUTABLE` 指定程序路径。测试覆盖 JavaScript 渲染、标签页 Cookie 共享、挑战超时后继续使用原浏览器，以及两个临时 profile 同时运行时的 Cookie 隔离。Linux 额外检查显式持久 profile 在浏览器重启后保留 Cookie。受管 Windows 环境可能无法访问系统 DPAPI，跨重启 Cookie 测试在非 Linux 系统跳过。默认 `go test ./...` 不需要浏览器。

## 管理命令

管理员私聊 bot：

| 命令 | 行为 |
| --- | --- |
| `/status` | 全部已知套餐、库存、价格、每项最后成功检查时间；过期数据提示，过长分条 |
| `/check` | 立即完整检查；运行中返回忙碌；也执行正常补货消息规则 |
| `/help`、`/start` | 显示帮助与检查间隔 |

订阅者不需要与 bot 私聊，频道不接受管理命令。配置仅通过宿主机环境变量/`.env` 修改。若只收到旧数据，请看最后检查时间和日志，旧数据不代表刚刚确认有货。

## 通知生命周期与可靠性

```text
首次 0             无通知，保存基线
0 → 3              SEND A
3 → 2 / 2 → 8      EDIT A，保留补货发现时间
8 → 0              EDIT A 为 ❌ 已售罄，成功后清除活动事件
0 → 0              不发不改
0 → 2              SEND B，永不把已关闭的 A 恢复成有货
2 → 0              EDIT B 为 ❌ 已售罄
```

首次启动已存在的正库存也会发送一次。库存与资料均不变时不调用 Telegram。活动期价格、周期、名称、订单链接等变化会编辑原消息。购买按钮显示「🛒 立即购买」，隐藏完整官方套餐 URL 并附加 `aff=1194`；售罄后显示「🛒 查看商品」。示例中的第三方短链、优惠码不会被自动采纳。

新消息失败不会设置活动消息 ID；编辑临时失败保留原 ID 并在后续检查重试；`message is not modified` 视为幂等成功。仅在明确的“消息不存在/无法编辑”错误时允许替换有货消息；售罄消息永久不存在时记录日志并结束本轮。`429 retry_after` 对后续请求生效，普通请求也有限速。

即使售罄编辑暂时失败，已观测到的零库存也会保存为待关闭事件；之后重新有货时仍先关闭 A，再发送 B。未观测到的短暂补货（发生在两次检查之间）无法获知。

**Telegram 与 JSON 之间无法实现严格的 exactly-once**：Telegram `sendMessage` 没有幂等键，超时不能判断服务端是否发出消息。程序在请求前持久化发送意图及 5 分钟冷却；不确定失败/请求中断后不会立即重发，之后库存仍为正会重试，极少数情况下仍可能重复。收到 message ID 后立即保存；保存失败则保留内存 ID、停止后续通知并优先重试磁盘保存。若在 Telegram 接受与落盘之间断电，仍有无法消除的重复窗口。售罄后不会发送过期的待重试有货通知。

## state.json 与解析方式

默认 `data/state.json`（容器内 `/data/state.json`）包括版本、规范化商品 key、最新商品观测、最近检查时间、已通知库存、活动消息的 chat/message ID、补货开始时间、已通知商品资料以及待关闭/重试记录。`product.availableQuantity` 是最近已观测库存，`lastAvailable` 是成功完成通知状态机的库存；Telegram 失败时二者可以暂时不同。

商品优先用 `host:pid:ID` 或规范化的官方购买路径作为身份，忽略大小写，不依赖数组位置。解析独立于 HTTP/状态机，按 WHMCS `.product` 商品容器及其标题/数量/价格/订单链接查找；容器类名变化时可用订单链接祖先关系回退。只信任明确数字 `N Available`。名称、价格、周期、同站 HTTPS 订单 URL 缺失会跳过该商品并警告；未知商品不会被补零。

浏览器先检查最终地址、文档状态码、Cloudflare 挑战标记、加载状态和 HTML 大小，再交给同一个 goquery 解析器。HTTP 错误、挑战超时、意外跨站跳转、浏览器进程退出均返回未知。默认 profile 由 chromedp 在临时目录中创建并在正常退出后清理，Cookie 在同一进程的检查间共享，重启后重新建立会话；库存和 Telegram 消息 ID 仍完整保存于 `state.json`。这可避免 Docker 容器重建后复用带旧主机名的 Chromium `SingletonLock`。

如确需跨重启保留浏览器 Cookie，可显式设置 `BROWSER_PROFILE_DIR=/data/browser`；同一目录只能由一个 Chromium 使用，所有旧容器停止后才可处理遗留锁。程序不会自动删除显式 profile 的锁，以免破坏仍在使用的资料。Cookie 可能过期或受出口 IP 限制，持久 profile 不代表可以永久通过挑战。不要提交或公开该目录。

写入先序列化到同目录 `state.json.tmp`，文件权限 0600，`fsync`/关闭后重命名，Linux 上再同步目录。无文件正常初始化；损坏文件先保留为 `.corrupt-时间戳` 再恢复，当前有货可能重新通知。未知版本拒绝覆盖以保护升级数据。读写权限错误应修复权限，不会悄悄丢弃旧状态。

### 添加 Spartan 分类

在 `.env` 添加 `SPARTAN_URLS`，用英文逗号分隔**全部**需要监控的分类地址；未设置时使用以下三个默认分类：

- `https://billing.spartanhost.net/store/cmin2-premium-kvm-vps-seattle`
- `https://billing.spartanhost.net/store/ddos-protected-ssd-premium-kvm-vps-seattle`
- `https://billing.spartanhost.net/store/ddos-protected-ssd-e5-kvm-vps-seattle`

修改后先执行 `-verify`，再 `docker compose up -d --force-recreate`。移除分类不会自动删除旧状态或宣布售罄，`/status` 中旧项会逐渐显示为过期。新供应商实现 `monitor.Provider` 返回相同商品模型即可，不需要改 Telegram 状态机。

## 常见故障

| 现象 | 检查方法 |
| --- | --- |
| bot 不回复 | 先私聊 `/start`；核对 token、`TELEGRAM_ADMIN_IDS` 中的 **用户 ID**；群组/频道命令及非管理员被忽略 |
| 频道收不到通知 | 核对 `TELEGRAM_CHANNEL_ID`，bot 的发布/编辑权限，以及是否真正发生补货；初始全为 0 不会发通知 |
| `401` / `403` Telegram 错误 | token 失效、bot 被屏蔽、频道 ID 或权限错误；修复后恢复检查 |
| `409 Conflict` | 有其他实例在轮询，或 webhook 未删除；只保留一个实例并删除旧 webhook |
| Spartan `403`、Cloudflare、HTML 变化 | 默认浏览器会执行页面 JavaScript 并等待；若仍超时，停止服务后在部署 VPS 执行 `-verify`。检查出口访问情况、等待或联系供应商确认允许访问；这些错误是未知库存 |
| TLS 模式仍为 `403` / challenge | 指纹模式无法执行 JavaScript 挑战；可用另一种支持的指纹做一次对照。不要缩短轮询间隔或把失败当作零库存；需要人工验证、可用出口或供应商放行 |
| Chromium 启动失败 | 核对 `BROWSER_EXECUTABLE`、profile 写入权限、是否有其他实例占用；Docker 中检查是否覆盖了镜像的 `BROWSER_NO_SANDBOX` 设置 |
| 浏览器 profile 锁定 | 新版本默认使用独立临时 profile；删除 `.env` 中显式的 `BROWSER_PROFILE_DIR` 可恢复默认。旧镜像可用 `-e BROWSER_PROFILE_DIR=/tmp/spartan-verify` 临时验证，不删除旧 profile。不要直接解锁仍被其他容器使用的目录 |
| 内存不足/浏览器退出 | 增加可用内存，检查 `docker stats`；下一次抓取会重建浏览器。不要删除 `state.json` |
| `/status` 一直显示旧库存 | 看每个商品的最后检查时间；HTTP/浏览器/解析失败时保留旧值是保护机制，查看 `inventory_fetch_or_parser_error` / `parser_warning` / `product_unknown` |
| `permission denied` | `sudo chown -R 65532:65532 data`；自定义 `STATE_FILE` 时同步调整挂载目录和权限 |
| Telegram 临时失败/限速 | 保留消息 ID 重试，不手动删除状态；不确定发送会等待 5 分钟，`429` 遵守等待时间 |
| 时间不对 | 设置有效 IANA `TZ`，默认 `Asia/Shanghai`；状态里 UTC 是正常的 |
| state 损坏恢复后重复通知 | 使用损坏备份排查磁盘问题；重新建立基线可能对现有正库存发一次消息 |

## 开发验证记录与限制

2026-10-21：已通过网页读取工具查看三个分类的服务端渲染内容和官方商品链接，每页显示 8 个套餐。普通 HTTP 请求均遇到 Cloudflare 403。随后已用本地 Chrome + 项目中的 chromedp 抓取器实际访问三个分类：浏览器成功启动并执行 JavaScript，但等待 35 秒后仍停留在挑战页；再用 `FETCH_MODE=browser` 的应用 `-verify` 逐页复测，三页日志均为 `Cloudflare challenge did not clear (last HTTP 403)`，验证按预期以非零状态退出。**无头模式已启用，但当前开发出口仍未成功取得三个真实商品页，不能宣称已解决 Cloudflare 拦截。**

精简 HTML 夹具是重建结构，不是原始页面快照，见 `testdata/README.md`。必须在部署出口跑 `-verify` 验证可达性与当前 DOM，必要时按真实 HTML 调整选择器。403、挑战超时和页面失败绝不会触发售罄。

本地离线测试覆盖生命周期、通知失败、售罄重试后再补货、JSON 重启、磁盘失败、损坏恢复、页面失败、管理员鉴权、长消息分割、时区和推广链接。新增浏览器复用、串行访问、进程断连恢复、启动/单页取消、挑战/状态码/重定向校验及子进程环境隔离测试。真实 Chrome 的本机集成测试已通过 JavaScript 渲染、Cookie 跨标签页共享、挑战超时后的浏览器复用，以及两个默认临时 profile 并存时的进程/Cookie 隔离；受管 Windows 环境使用 `BROWSER_NO_SANDBOX=true` 才能启动，Linux 专用跨重启 Cookie 测试在此跳过。构建采用 Go 1.27.3。Docker Engine 在开发环境未安装，镜像构建和 Linux 显式持久 profile 的持久化仍需在 Debian/Docker 环境验证。未使用真实 Telegram 凭据，也未发送任何测试通知。

TLS 模式离线 HTTPS 测试覆盖两种指纹的 HTTP/2 协商、Chrome ClientHello GREASE、配套请求头、gzip 解压、Cookie/相对重定向、证书校验、取消、响应体超时、403/500/挑战页/布局变化/空响应/过大响应，以及跨站、HTTP 降级、带凭据跳转和重定向循环；失败保留未知，真实零库存仍可解析。TLS 模式的三页真实访问尝试被本地网络权限在 TCP 建连阶段拒绝（`connectex: access permissions`），尚未验证能否通过 Spartan 的 CF；请在部署 VPS 上运行上述 `FETCH_MODE=tls ... -verify` 命令确认。网页读取工具能够读取公开页面，不等同于此 Go 客户端或 VPS 出口验证成功。
