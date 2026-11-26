# superlight-uma

**一个开源、超轻量的匿名活跃设备（心跳）统计框架。**

源码本身不属于任何具体产品：任何人都可以从源码编译、部署一份，再用**这份部署自己的配置文件**（环境变量）决定它为哪些应用服务、谁能查看数据、允许哪些网页跨域访问。一份部署 = 一个实例；同一套代码可以部署出任意多个互不相干的实例。

- Go 编写，单个静态二进制，无 CGO，常驻内存约 10–20 MB
- 客户端只上报 `app_id` + 本地随机生成的匿名设备 UUID；没有账号、不收集个人信息、不存储 IP
- 服务端按 UTC 自然日去重，计算日活（DAU）、周活（WAU，最近 7 天）、月活（MAU，最近 30 天）和历史总设备数
- 自带一个纯静态、无需构建的数据面板（`web/`），可直接部署到 Cloudflare Pages 等静态托管

## 目录

1. [它是什么](#它是什么)
2. [一份部署如何成为一个实例](#一份部署如何成为一个实例)
3. [配置参考](#配置参考)
4. [API](#api)
5. [客户端接入](#客户端接入)
6. [运行与部署](#运行与部署)
7. [数据面板](#数据面板webcloudflare-pages)
8. [目录结构](#目录结构)

## 它是什么

一个只做一件事的统计服务：**数有多少台设备在用你的应用**。

- 通用上报接口：任何应用（桌面、移动、网页、命令行……）都可以接入，按 `app_id` 区分，数据互相独立
- 白名单：只接受实例配置里登记过的 `app_id`，防止被乱刷
- 同一设备一天上报多少次都只算一次
- 存储不会无限增长：
  - `daily_activity`（每天哪些设备活跃过）只保留最近 31 天，自动清理
  - `daily_summary`（每天的 DAU/WAU/MAU/总数汇总）永久保留，用于看历史趋势
  - `devices`（每台设备一行：首次 / 最后出现时间、最近一次的版本和平台）
- 默认使用 SQLite（纯 Go 驱动 `modernc.org/sqlite`），存储层是接口，可替换为 Postgres 等
- 管理接口可用管理密钥（Bearer Token）保护，也可以不设密钥公开访问
- 按 IP 限流、请求体大小限制、可配置 CORS、健康检查、优雅退出

它适合"只想知道有多少活跃设备"的场景。如果需要事件分析、漏斗、留存等完整产品分析，请考虑 PostHog、Aptabase 等完整平台。

## 一份部署如何成为一个实例

代码里没有写死任何应用、域名或密钥。一个实例的全部"身份"都来自它自己的配置：

| 实例要回答的问题 | 由哪个配置决定 |
| --- | --- |
| 为哪些应用统计？ | `UMA_ALLOWED_APPS`，如 `my-desktop-app,my-android-app` |
| 谁能看数据？ | `UMA_ADMIN_KEY`：设置后查询接口需要密钥；留空则公开 |
| 哪些网页可以从浏览器调用？ | `UMA_CORS_ORIGINS`，如 `https://stats.example.com,https://*.pages.dev` |
| 数据存在哪？ | `UMA_DB_PATH` |
| 监听在哪、是否在反向代理后面？ | `UMA_ADDR`、`UMA_TRUST_PROXY` |

一个典型实例的配置文件（systemd 部署时位于 `/etc/superlight-uma/superlight-uma.env`，示例见 `deploy/superlight-uma.env.example`）：

```bash
UMA_ADDR=127.0.0.1:8082
UMA_DB_PATH=/var/lib/superlight-uma/uma.db
UMA_ALLOWED_APPS=my-desktop-app,my-android-app
UMA_TRUST_PROXY=true
UMA_CORS_ORIGINS=https://stats.example.com
UMA_ADMIN_KEY=<openssl rand -hex 32 生成>
```

改配置 → 重启服务，实例就变了：

- **新增一个接入应用**：把新的 app_id 加到 `UMA_ALLOWED_APPS`（逗号分隔）并重启，各应用数据互相独立
- **开放 / 关闭查询鉴权**：清空或设置 `UMA_ADMIN_KEY` 并重启
- **允许新的面板域名**：加到 `UMA_CORS_ORIGINS` 并重启

> 仓库里没有任何具体实例的信息：`deploy/install.sh` 通过环境变量接收实例的域名、app_id 和跨域来源（`UMA_DOMAIN`、`UMA_APPS`、`UMA_CORS`、`UMA_PORT`，见[直接部署](#直接部署systemd推荐小内存服务器)），`deploy/Caddyfile.snippet` 里的 `uma.example.com` 会在安装时被替换。
>
> 数据面板 `web/` 的源码里没有任何实例信息，接口地址和默认应用通过 [Cloudflare Pages 环境变量](#cloudflare-pages-环境变量) 在构建时注入。

## 配置参考

命令行参数优先于环境变量。

| 环境变量 | 参数 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `UMA_ADDR` | `-addr` | `:8080` | 监听地址 |
| `UMA_DB_PATH` | `-db` | `uma.db` | SQLite 文件路径 |
| `UMA_ALLOWED_APPS` | `-apps` | （必填） | 允许上报的 app_id，逗号分隔，如 `my-app,other-app`（小写字母、数字、`-_.`，最长 64） |
| `UMA_ADMIN_KEY` | `-admin-key` | （空） | 管理接口（`/v1/stats`、`/v1/stats/history`、`/v1/apps`）的密钥，设置时至少 16 位。**留空则管理接口无需密钥、任何人都能查看统计**（启动日志会给出警告），只建议在内网、测试或确实想公开数据时使用 |
| `UMA_RATE_LIMIT` | `-rate-limit` | `1` | 每个 IP 每秒允许的心跳数，`0` 关闭限流 |
| `UMA_RATE_BURST` | `-rate-burst` | `20` | 限流突发量 |
| `UMA_TRUST_PROXY` | `-trust-proxy` | `false` | 在反向代理后面时设为 `true`，用 `X-Forwarded-For` 识别客户端 IP（仅用于限流，不存储） |
| `UMA_CORS_ORIGINS` | `-cors-origins` | （空） | 允许浏览器跨域调用的来源（上报和管理接口都适用），逗号分隔。支持精确地址 `https://stats.example.com`、通配子域名 `https://*.pages.dev`、或 `*`（任意）。留空则关闭 CORS；桌面/原生应用上报不需要 |
| `UMA_ROLLUP_EVERY` | `-rollup-every` | `1h` | 计算每日汇总和清理过期数据的间隔（最小 1m） |
| `UMA_RETENTION_DAYS` | `-retention-days` | `31` | 每日活跃明细保留天数（最小 31，MAU 需要） |

生成管理密钥：

```bash
openssl rand -hex 32
```

## API

### 上报心跳（无需密钥）

```
POST /v1/heartbeat
Content-Type: application/json

{
  "app_id": "my-app",                                   // 必填，必须在实例的 UMA_ALLOWED_APPS 内
  "device_id": "3f1c2b4a-1d2e-4f60-8a9b-0c1d2e3f4a5b",  // 必填，UUID（8-4-4-4-12 格式）
  "version": "1.2.0",                                   // 可选，≤64 字符
  "platform": "windows"                                 // 可选，≤64 字符
}
```

| 状态码 | 含义 |
| --- | --- |
| 204 | 成功（无响应体） |
| 400 | JSON 无效 / device_id 不是 UUID / 字段过长 |
| 403 | app_id 未登记 |
| 413 | 请求体超过 4KB |
| 429 | 触发限流（带 `Retry-After`） |

### 查询当前统计（设置了管理密钥时需要）

```
GET /v1/stats?app_id=my-app
Authorization: Bearer <UMA_ADMIN_KEY>

→ 200
{"app_id":"my-app","day":"2026-11-25","dau":1234,"wau":5678,"mau":9012,"total":20000}
```

- `dau`：今天（UTC）活跃的不同设备数
- `wau`：最近 7 天（含今天）活跃的不同设备数
- `mau`：最近 30 天（含今天）活跃的不同设备数
- `total`：历史上见过的所有设备数

### 查询历史趋势（设置了管理密钥时需要）

```
GET /v1/stats/history?app_id=my-app&days=30
Authorization: Bearer <UMA_ADMIN_KEY>

→ 200
{"app_id":"my-app","days":[{"app_id":"my-app","day":"2026-11-24","dau":...,"wau":...,"mau":...,"total":...}, ...]}
```

`days` 默认 30，范围 1–3650。汇总每小时（可配置）计算一次：昨天的数据在跨日后的第一次计算时定稿，今天的数据是阶段值，会被后续计算覆盖。

### 列出已登记应用（设置了管理密钥时需要）

```
GET /v1/apps
Authorization: Bearer <UMA_ADMIN_KEY>

→ 200 {"apps":["my-app","other-app"]}
```

### 鉴权规则

- 实例设置了 `UMA_ADMIN_KEY`：上面三个查询接口必须带正确的 `Authorization: Bearer <key>`，否则返回 401
- 实例没有设置（或为空）：三个查询接口无需密钥，任何 `Authorization` 头都会被忽略
- 上报接口和健康检查始终无需密钥

### 健康检查

```
GET /healthz → 200 {"status":"ok"}
```

## 客户端接入

### 原则

1. **首次启动**时本地生成一个 UUID v4，持久化保存（配置文件 / 本地存储），以后一直用同一个。UUID v4 是纯随机离线生成的，不需要联网查重。
2. **启动时**上报一次，之后**每隔几小时**（建议 4–6 小时）上报一次。一天内多次上报只算一次活跃。
3. 上报失败直接忽略，不要重试轰炸，下次定时再报即可。不要阻塞应用主流程。
4. 想让用户"重置统计身份"时，删除本地保存的 UUID 重新生成即可。
5. `app_id` 必须是实例 `UMA_ALLOWED_APPS` 里登记过的；不同平台想分开统计，就各用一个 app_id（如 `my-app-desktop`、`my-app-android`）。

### 浏览器 / Electron / 网页（JavaScript）

```js
const UMA_URL = "https://uma.example.com/v1/heartbeat";
const APP_ID = "my-app";
const KEY = "uma_device_id";

function getDeviceId() {
  let id = localStorage.getItem(KEY); // Electron 主进程可改为存到 userData 目录下的文件
  if (!id) {
    id = crypto.randomUUID(); // UUID v4，离线随机生成
    localStorage.setItem(KEY, id);
  }
  return id;
}

async function sendHeartbeat() {
  try {
    await fetch(UMA_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        app_id: APP_ID,
        device_id: getDeviceId(),
        version: "1.2.0",
        platform: navigator.userAgentData?.platform ?? navigator.platform,
      }),
      keepalive: true,
    });
  } catch (_) {
    // 忽略失败，下次再报
  }
}

sendHeartbeat();                              // 启动时
setInterval(sendHeartbeat, 4 * 60 * 60 * 1000); // 每 4 小时
```

> 纯网页从其他域名上报时，实例需要在 `UMA_CORS_ORIGINS` 里允许该网页的来源。

### Go 客户端

```go
id := loadOrCreateID() // 首次 uuid.NewString()（github.com/google/uuid），写入本地配置文件
body, _ := json.Marshal(map[string]string{
	"app_id": "my-app", "device_id": id, "version": version, "platform": runtime.GOOS,
})
resp, err := http.Post("https://uma.example.com/v1/heartbeat", "application/json", bytes.NewReader(body))
if err == nil {
	resp.Body.Close()
}
```

### Rust（桌面）与 Kotlin（Android）

> 目前仓库还**没有** Rust 和 Kotlin 的示例代码。接入只需要按下面的原始请求格式自己实现（通常二三十行）：
>
> - 请求：`POST https://<实例地址>/v1/heartbeat`，`Content-Type: application/json`，无需密钥
> - 请求体：`{"app_id": "...", "device_id": "<UUID v4>", "version": "...", "platform": "..."}`
>   - `app_id`、`device_id` 必填；`version`、`platform` 可选（各 ≤64 字符），整个请求体 ≤4KB
> - 响应：`204` 成功；`400` JSON 无效 / UUID 格式错误 / 字段过长；`403` app_id 未登记；`429` 限流（稍后再报）
> - 设备 ID：首次运行生成 UUID v4 并持久化（Rust 可用 `uuid` crate 存到配置目录；Android 可用 `java.util.UUID.randomUUID()` 存到 SharedPreferences / DataStore），之后一直复用
> - 时机：启动时报一次，长时间运行时每几小时再报一次；失败直接忽略，放在后台线程 / 协程里，不阻塞主流程

### curl 测试

```bash
curl -i -X POST http://localhost:8080/v1/heartbeat \
  -H 'Content-Type: application/json' \
  -d '{"app_id":"my-app","device_id":"'"$(uuidgen | tr A-Z a-z)"'","platform":"linux"}'

curl -s 'http://localhost:8080/v1/stats?app_id=my-app' \
  -H "Authorization: Bearer $UMA_ADMIN_KEY"   # 实例未设置密钥时可省略这一行
```

## 运行与部署

需要 Go 1.26+。

### 本地运行

```bash
go test ./...
CGO_ENABLED=0 go build -o superlight-uma ./cmd/superlight-uma

UMA_ALLOWED_APPS=my-app \
UMA_ADMIN_KEY=$(openssl rand -hex 32) \
UMA_DB_PATH=./uma.db \
./superlight-uma
```

### 直接部署（systemd，推荐小内存服务器）

`deploy/` 目录是一套可直接使用的 systemd 部署方案，实例信息通过环境变量传给 `install.sh`：

| 文件 | 说明 |
| --- | --- |
| `deploy/superlight-uma.service` | systemd 服务：专用用户 `superlight-uma`、`Restart=on-failure`、`MemoryMax=64M`、`ProtectSystem=strict` 等加固 |
| `deploy/superlight-uma.env.example` | 实例配置文件示例，部署后位于 `/etc/superlight-uma/superlight-uma.env`（0640，root:superlight-uma） |
| `deploy/Caddyfile.snippet` | 追加到 `/etc/caddy/Caddyfile` 的站点配置模板（`uma.example.com` 在安装时替换为 `UMA_DOMAIN`） |
| `deploy/install.sh` | 安装 / 升级脚本 |

服务器布局：二进制 `/opt/superlight-uma/superlight-uma`（升级时旧版本保留为 `superlight-uma.prev`），数据 `/var/lib/superlight-uma/uma.db`，监听 `127.0.0.1:8082`，由 Caddy 提供 HTTPS。

```bash
# 在开发机上编译（静态二进制）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -X main.version=$(git rev-parse --short HEAD)" \
  -o superlight-uma ./cmd/superlight-uma

# 上传并安装（首次安装会在服务器上用 openssl 生成管理密钥，写入实例配置文件，不会打印）
scp -r superlight-uma deploy <user>@<server>:/tmp/uma/
ssh <user>@<server> 'sudo UMA_DOMAIN=uma.example.com UMA_APPS=my-app /tmp/uma/deploy/install.sh /tmp/uma/superlight-uma --caddy'
```

| `install.sh` 环境变量 | 默认值 | 作用 |
| --- | --- | --- |
| `UMA_DOMAIN` | （无，使用 `--caddy` 时必填） | Caddy 站点域名 |
| `UMA_APPS` | `my-app` | 首次安装时写入实例配置的 `UMA_ALLOWED_APPS` |
| `UMA_CORS` | `https://*.pages.dev,http://localhost:8000` | 实例配置缺少 `UMA_CORS_ORIGINS` 时写入的值 |
| `UMA_PORT` | `8082` | 首次安装时的本地监听端口（`UMA_ADDR=127.0.0.1:<port>`） |

这些变量只在**首次安装**（或配置项缺失）时写入实例配置文件；之后升级不会覆盖已有配置，实例的真实值始终以服务器上的 env 文件为准。

- 升级：重复上面两步即可。升级不会覆盖已有的实例配置，只会补上新版本引入的缺失配置项；`--caddy` 在配置已存在时不会重复追加。
- 回滚：`sudo cp -a /opt/superlight-uma/superlight-uma.prev /opt/superlight-uma/superlight-uma && sudo systemctl restart superlight-uma`
- 查看管理密钥：`sudo grep UMA_ADMIN_KEY /etc/superlight-uma/superlight-uma.env`
- 修改实例配置：编辑上面的 env 文件后 `sudo systemctl restart superlight-uma`
- 日志：`journalctl -u superlight-uma -f`

### Docker

```bash
docker build -t superlight-uma .
docker run -d --name uma -p 8080:8080 \
  -v uma-data:/data \
  -e UMA_ALLOWED_APPS=my-app \
  -e UMA_ADMIN_KEY=替换为你的密钥 \
  superlight-uma
```

数据库在容器内 `/data/uma.db`，务必挂载卷持久化。实例配置也可以用 `--env-file` 传入。

### 生产建议

- 前面放一个 HTTPS 反向代理（Caddy / Nginx），并设置 `UMA_TRUST_PROXY=true`
- SQLite 只需单实例运行；定期备份 `uma.db`（例如 `sqlite3 uma.db ".backup backup.db"`）
- 存储规模：明细约为「活跃设备数 × 31 天」行，设备表每台设备一行，100 万台设备约几十 MB
- 服务停机超过一天时，停机期间的日汇总不会补算（实时 `/v1/stats` 不受影响）

## 数据面板（web/，Cloudflare Pages）

`web/` 是一个纯静态、无需构建的管理面板（原生 HTML + JS + CSS，图表为手写 SVG，不依赖任何 CDN，国内网络也能用），可以连接任意一个实例：

- 登录：填写实例的接口地址和管理密钥，可选择在本浏览器记住（localStorage）；"退出"会清除保存的密钥
- 免密模式：实例没有设置 `UMA_ADMIN_KEY` 时，打开页面会先不带密钥探测 `/v1/apps`，成功就直接进入面板（右上角按钮变为"设置"）；登录框里密钥也可以留空
- 应用选择：自动从 `/v1/apps` 读取已登记应用，也可以手动输入 app_id
- 今日 DAU / WAU / MAU / 总设备卡片
- 趋势图（7 / 30 / 90 / 365 天，可切换显示各指标，悬停或点按查看数值）和每日明细表
- 适配手机，跟随系统深色模式
- 错误提示：密钥错误（401）、未登记应用、网络不通 / CORS 未允许等

面板连接哪个实例由 `web/config.js` 决定，它由 `web/build.sh` 根据环境变量生成（见下）。仓库里提交的是空配置：不跑构建、直接上传文件夹也能用，只是需要在登录框里手动填接口地址。

### 部署到 Cloudflare Pages

方式一：连接 GitHub 仓库（推送后自动部署）

1. Cloudflare 控制台 → Workers & Pages → Create → Pages → Connect to Git，选择你的仓库（需要给 Cloudflare 的 GitHub App 授权访问）
2. Production branch：`main`
3. Framework preset：`None`；Build command：`sh web/build.sh`；Build output directory：`web`
4. 在 Settings → Variables and Secrets（环境变量）里按下表设置 `UMA_API_BASE`、`UMA_APP_ID`，然后重新部署
5. Save and Deploy，得到 `https://<项目名>.pages.dev`

方式二：直接上传（不连仓库）

- 控制台 Create → Pages → Upload assets，把 `web` 文件夹拖进去；或命令行 `npx wrangler pages deploy web --project-name uma-dashboard`
- 直接上传时不会执行构建命令。想预置接口地址，先在本地运行 `UMA_API_BASE=https://uma.example.com UMA_APP_ID=my-app sh web/build.sh` 再上传（生成的 `config.js`、`_headers` 不要提交回仓库，可用 `sh web/build.sh` 恢复空配置）

### Cloudflare Pages 环境变量

| 设置项 | 值 |
| --- | --- |
| Build command | `sh web/build.sh` |
| Build output directory | `web` |

| 变量 | 是否必填 | 示例 | 作用 |
| --- | --- | --- | --- |
| `UMA_API_BASE` | 可选 | `https://uma.example.com` | 实例的接口地址，作为登录框的默认值；同时写进 CSP 的 `connect-src`，页面只能连接这个地址。必须是 `https://`（本地测试可用 `http://localhost`），可以带端口和路径前缀 |
| `UMA_APP_ID` | 可选 | `my-app` | 默认打开的应用（需在实例 `UMA_ALLOWED_APPS` 内） |

- `build.sh` 是不依赖任何工具的 POSIX shell 脚本（无 npm），会校验变量格式，不合法时构建失败
- 生成 `web/config.js`（`window.UMA_CONFIG = {apiBase, appId}`，作为同源脚本加载，CSP 仍是 `script-src 'self'`）和 `web/_headers`（由 `web/_headers.template` 生成）
- 不设 `UMA_API_BASE`：登录时手动填接口地址；CSP 的 `connect-src` 放宽为 `'self' https: http://localhost:* http://127.0.0.1:*`，可以连任意 https 接口，安全性比锁定单一地址弱一些
- 不设 `UMA_APP_ID`：自动选中 `/v1/apps` 返回的第一个应用
- 修改变量后需要在 Pages 里重新部署才会生效

其他静态托管（Nginx、GitHub Pages、对象存储等）同样可以直接托管 `web/` 目录；`_headers` 是 Cloudflare Pages 的格式，换平台时需要自行设置等效的安全响应头。

### 跨域与安全设置

- 实例的 `UMA_CORS_ORIGINS` 必须包含面板的网址，例如 `https://*.pages.dev,http://localhost:8000`（`*.pages.dev` 意味着任意 pages.dev 子域名都能发起请求；设置了管理密钥时，没有密钥也看不到数据）。
- 绑定自定义域名（如 `stats.example.com`）后，把它加进实例配置的 `UMA_CORS_ORIGINS`（逗号分隔）并重启服务。确定域名后可以去掉 `https://*.pages.dev` 收紧范围。
- `web/_headers` 设置了 CSP 等安全响应头。设置了 `UMA_API_BASE` 时 `connect-src` 只允许该接口地址；实例换了地址，改环境变量并重新部署即可。
- 建议在 Cloudflare Zero Trust（Access）里给面板域名加一层登录保护（免费），这样面板本身也不对外公开。

### 本地预览

```bash
cd web && python3 -m http.server 8000
# 打开 http://localhost:8000 （需要实例的 UMA_CORS_ORIGINS 包含 http://localhost:8000）
```

## 目录结构

```
cmd/superlight-uma/      程序入口（配置、HTTP 服务、优雅退出、定时汇总）
internal/config/         环境变量 / 命令行参数解析与校验
internal/server/         HTTP 接口、鉴权、限流、CORS、每日汇总与清理
internal/store/          存储接口定义
internal/store/sqlite/   SQLite 实现
deploy/                  systemd / Caddy / 安装脚本（示例实例）
web/                     静态数据面板（Cloudflare Pages）
```

## 许可证

[MIT](LICENSE)
