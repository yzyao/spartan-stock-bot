# Spartan Cloudflare 独立对照实验

仅复现两个 GitHub 项目的抓取路径，不部署它们的数据库、前端、统计上传或通知功能。实验没有 Telegram 配置，不读取 `.env`/`state.json`，不加购物车、不下单。默认仅检查 CMIN2 一页；`--all` 才检查三个分类，每条路径串行访问一次，没有轮询和自动重复尝试。

| 名称 | 对照来源与流程 |
| --- | --- |
| `stockvps`（第一个项目） | [izhichao/stockvps-monitor](https://github.com/izhichao/stockvps-monitor/blob/f9e1671e32e3565851ac13075525bc1d31a6cc14/monitor.js)：`impit` Chrome → 403 → FlareSolverr → 缓存匹配的 Cookie 和同一 User-Agent |
| `vpsknow`（最后一个项目） | [everett7623/vpsknow-stock](https://github.com/everett7623/vpsknow-stock/blob/00f821de4392bdca4b784c0e841c8e0114bf17e1/packages/providers/src/http.ts)：Node fetch → 失败/挑战 → curl IPv4、压缩、浏览器 UA；分类 URL 加 `language=english` |

这是按上述算法改写的独立探针，并非运行完整上游项目。保留其主要传输流程；额外加了超时、响应大小限制、同源/HTTPS 检查及严格商品验证。使用上游依赖范围内固定版本 `impit 0.14.1`、`cheerio 1.2.0`，FlareSolverr 固定 `v3.5.2`。Node 仅在实验容器中使用，主项目仍是 Go。

## 在部署 VPS 上执行

要求 Docker Engine + Compose 插件和 Bash。无需宿主机安装 Node/Python，也无需重新构建主 bot 的 Go 镜像。首次会下载实验镜像和 npm 依赖，并构建一个 Node + curl 小环境；后续使用 Docker 构建缓存。

```bash
cd /opt/spartan-stock-bot
git pull --ff-only origin main

# 正式监控若正在运行，先暂停，以免同时请求供应商
docker compose stop

# 默认比较两条路径，只请求 CMIN2 一页
bash experiments/cloudflare/run.sh both
```

出现 `success:true` 且有真实商品后，再验证三页：

```bash
bash experiments/cloudflare/run.sh both --all
```

也可分别执行：

```bash
bash experiments/cloudflare/run.sh vpsknow --all
bash experiments/cloudflare/run.sh stockvps --all
```

每轮使用独立 Compose 项目名，无宿主机端口映射；solver 只对实验网络开放，不挂载 bot 数据。结束/中断自动移除本轮实验容器与网络，保留镜像作为缓存；不会启动或停止正式 bot。失败时会输出最后 30 行 solver 日志。首次 solver 启动最多等待 60 秒，单次挑战处理最多 60 秒，每条路径总耗时与成功页面数、挑战等待有关。

如已有明确可用的代理，可在运行前设置 `PROBE_PROXY_URL=http://host:port`（支持 HTTP/HTTPS/SOCKS5，不接受 URL 凭据）；不设置时直接访问。`127.0.0.1` 在容器内是该容器自身，不是宿主机。不要在终端日志里粘贴代理密码。

## 如何判断结果

标准输出为 JSON 行：

- `attempt`：使用的传输路径、HTTP 状态和是否复用已验证 Cookie，不输出 Cookie 内容。
- `page_result`：`success`、分类、套餐、真实数字库存、价格/周期、官方订单 URL、耗时。
- `experiment_completed`：各路径、各页的结果列表。

只有明确的商品卡片同时具备名称、数字 `N Available`、价格、账期、同源 HTTPS 购买链接时才算解析成功。真实 `0 Available` 是有效库存，不因 Order Now 按钮改成有货。未知 HTML、缺字段、403、挑战页、超时均失败；一张商品卡解析不完整会产生 warning，本页不算完整通过。

**FlareSolverr 的 `solution.status` 在上游源码中可能写死为 200。** 因此输出把它标记为 `reportedHTTPStatus`、`actualHTTPStatusKnown:false`，最终仍用商品 DOM 检查；`status:ok`/“Challenge solved”/200 都不能单独证明成功。

全部选择的路径/页面完整通过才返回退出码 `0`；任何失败返回 `1`，参数错误返回 `2`。一条路径成功、另一条失败时总退出码仍为 1，以每条 `page_result` 为准。

单次通过只证明当时出口可抓取；无人值守可用性还需要后续重复验证 Cookie 过期、重启和连续抓取。实验不会自动把任何路径启用为生产模式。

## 本地开发检查

```bash
cd experiments/cloudflare
npm ci --ignore-scripts
npm test
# 有 Node 24+ 和 curl 时可以直接运行；stockvps 的 403 fallback 还需可访问的 solver
node probe.mjs vpsknow
```

测试使用模拟 HTTP 响应及本地 HTTP 服务，不访问 Spartan。覆盖 fallback、Cookie/UA 缓存、伪 200/挑战页、真实零库存、缺字段、跨源跳转、响应大小和原生 impit 绑定。当前开发环境没有 Docker，无法在本机执行 solver 镜像；不能把本地单元测试当作已通过 Spartan CF。

开发机单页真实尝试：`impit` 返回 403；Node fetch/curl 在 Windows 环境未成功取得响应；本机没有运行 FlareSolverr 服务，完整第一条路径因此尚未实测完成。请以部署 VPS 上实验输出的商品 DOM 验证结果为准。
