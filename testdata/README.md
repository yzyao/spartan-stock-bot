# 离线 HTML 夹具

`products.html` 是按 2026-10-21 网页读取结果中的套餐字段和 WHMCS 商品卡结构整理的精简夹具，**不是成功下载的原始页面快照**。覆盖嵌套标签、换行、可选硬件缺失、相对/绝对购买链接和库存解析失败。

开发环境普通 HTTP 请求三个分类 URL 均遇到 Cloudflare HTTP 403，因此未能保存实际源 HTML。应用现已默认使用 Go + chromedp 驱动无头 Chromium。上线前应在部署 VPS 执行 `-verify`，确认实际 DOM 与解析器匹配；程序不会把 403、挑战超时或解析失败转换成库存 0。

浏览器集成测试只启动本机 HTTP 测试服务器，模拟 JavaScript 渲染和持续挑战页面，不访问真实 Spartan 或 Telegram。用 `SPARTAN_BROWSER_TESTS=1` 显式运行。
