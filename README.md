# superlight-uma

**An open-source, ultra-lightweight framework for counting anonymous active devices (heartbeats). 一个开源、超轻量的匿名活跃设备（心跳）统计框架。**

English | [简体中文](README.zh-CN.md)

The source code is not tied to any particular product. Anyone can build and deploy it from source, and **each deployment's own configuration** (environment variables) decides which apps it serves, who can read the data, and which web pages may call it from a browser. One deployment = one instance; the same code can be deployed as any number of independent instances.

- Written in Go: a single static binary, no CGO, roughly 10–20 MB resident memory
- Clients only send an `app_id` plus an anonymous, locally generated random device UUID; no accounts, no personal data, no IP addresses stored
- The server de-duplicates per UTC calendar day and computes daily active (DAU), weekly active (WAU, last 7 days) and monthly active (MAU, last 30 days) devices, plus the all-time device total
- Ships with a static dashboard (`web/`, no npm dependencies, just an optional POSIX sh build script that injects the instance settings) that can be hosted on Cloudflare Pages or any static host

## Contents

1. [What it is](#what-it-is)
2. [Architecture](#architecture)
3. [How a deployment becomes an instance](#how-a-deployment-becomes-an-instance)
4. [Configuration reference](#configuration-reference)
5. [API](#api)
6. [Client integration](#client-integration)
7. [Running and deploying](#running-and-deploying)
8. [Dashboard](#dashboard-web-cloudflare-pages)
9. [Directory layout](#directory-layout)
10. [License](#license)

## What it is

A stats service that does one thing: **count how many devices are using your app**.

- Generic reporting endpoint: any app (desktop, mobile, web, CLI…) can integrate; data is separated by `app_id`
- Allow-list: only `app_id`s registered in the instance configuration are accepted, which prevents spam
- A device is counted once per day no matter how many times it reports
- Storage does not grow without bound:
  - `daily_activity` (which devices were active on which day) keeps only the last 31 days and is cleaned up automatically
  - `daily_summary` (daily DAU/WAU/MAU/total) is kept forever for historical trends
  - `devices` (one row per device: first / last seen, latest version and platform)
- SQLite by default (pure-Go driver `modernc.org/sqlite`); the storage layer is an interface and can be swapped for Postgres etc.
- Admin endpoints can be protected with an admin key (Bearer token), or left open with no key
- Per-IP rate limiting, request size limit, configurable CORS, health check, graceful shutdown

It fits the "I just want to know how many active devices I have" use case. For event analytics, funnels, retention and other full product analytics, consider a full platform such as PostHog or Aptabase.

## Architecture

```
 Clients (desktop / mobile / web / CLI)
        │  POST /v1/heartbeat {app_id, device_id}   ← no key needed
        ▼
 HTTPS reverse proxy (Caddy / Nginx, optional CDN)
        ▼
 superlight-uma (single Go binary) ── SQLite file (uma.db)
        ▲
        │  GET /v1/stats · /v1/stats/history · /v1/apps   ← admin key optional
 Static dashboard web/ (Cloudflare Pages or any static host)
```

- **Server**: one Go process that validates reports, de-duplicates per day, rolls up hourly and prunes expired detail rows; all configuration comes from environment variables / command-line flags
- **Storage**: a single SQLite file by default, no separate database server; the storage layer is an interface and can be replaced
- **Dashboard**: static pages that call the server's query endpoints directly from the browser; the API address and default app are injected at build time from environment variables
- **Deployment model**: the same source code becomes a concrete instance through the server's env file plus the static host's build variables

## How a deployment becomes an instance

No app, domain or key is hard-coded. An instance's entire "identity" comes from its own configuration:

| Question the instance answers | Decided by |
| --- | --- |
| Which apps are counted? | `UMA_ALLOWED_APPS`, e.g. `my-desktop-app,my-android-app` |
| Who can read the data? | `UMA_ADMIN_KEY`: when set, query endpoints require the key; when empty, they are public |
| Which web pages may call it from a browser? | `UMA_CORS_ORIGINS`, e.g. `https://stats.example.com,https://*.pages.dev` |
| Where is the data stored? | `UMA_DB_PATH` |
| Where does it listen, and is it behind a reverse proxy? | `UMA_ADDR`, `UMA_TRUST_PROXY` |

A typical instance configuration file (at `/etc/superlight-uma/superlight-uma.env` for a systemd deployment; see `deploy/superlight-uma.env.example`):

```bash
UMA_ADDR=127.0.0.1:8082
UMA_DB_PATH=/var/lib/superlight-uma/uma.db
UMA_ALLOWED_APPS=my-desktop-app,my-android-app
UMA_TRUST_PROXY=true
UMA_CORS_ORIGINS=https://stats.example.com
UMA_ADMIN_KEY=<generated with openssl rand -hex 32>
```

Change the config → restart the service, and the instance changes:

- **Add an app**: append the new app_id to `UMA_ALLOWED_APPS` (comma-separated) and restart; each app's data is independent
- **Open / close query auth**: clear or set `UMA_ADMIN_KEY` and restart
- **Allow a new dashboard domain**: add it to `UMA_CORS_ORIGINS` and restart

> The repository contains no instance-specific information: `deploy/install.sh` receives the instance's domain, app_ids and CORS origins via environment variables (`UMA_DOMAIN`, `UMA_APPS`, `UMA_CORS`, `UMA_PORT`, see [Direct deployment](#direct-deployment-systemd-recommended-for-small-servers)), and the `uma.example.com` placeholder in `deploy/Caddyfile.snippet` is replaced at install time.
>
> The dashboard source in `web/` contains no instance information either; the API address and default app are injected at build time through [Cloudflare Pages environment variables](#cloudflare-pages-environment-variables).

## Configuration reference

Command-line flags take precedence over environment variables.

| Env var | Flag | Default | Description |
| --- | --- | --- | --- |
| `UMA_ADDR` | `-addr` | `:8080` | Listen address |
| `UMA_DB_PATH` | `-db` | `uma.db` | SQLite file path |
| `UMA_ALLOWED_APPS` | `-apps` | (required) | Allowed app_ids, comma-separated, e.g. `my-app,other-app` (lowercase letters, digits, `-_.`, max 64 chars) |
| `UMA_ADMIN_KEY` | `-admin-key` | (empty) | Key for the admin endpoints (`/v1/stats`, `/v1/stats/history`, `/v1/apps`); at least 16 characters when set. **When empty, the admin endpoints need no key and anyone can read the stats** (a warning is logged at startup); only recommended on internal networks, for testing, or when you really want public stats |
| `UMA_RATE_LIMIT` | `-rate-limit` | `1` | Heartbeats allowed per IP per second; `0` disables rate limiting |
| `UMA_RATE_BURST` | `-rate-burst` | `20` | Rate-limit burst size |
| `UMA_TRUST_PROXY` | `-trust-proxy` | `false` | Set to `true` behind a reverse proxy to use `X-Forwarded-For` for the client IP (used only for rate limiting, never stored) |
| `UMA_CORS_ORIGINS` | `-cors-origins` | (empty) | Origins allowed to call from a browser (applies to both reporting and admin endpoints), comma-separated. Supports exact origins `https://stats.example.com`, wildcard subdomains `https://*.pages.dev`, or `*` (any). Empty disables CORS; desktop / native app reporting does not need it |
| `UMA_ROLLUP_EVERY` | `-rollup-every` | `1h` | Interval for computing daily summaries and pruning expired data (min 1m) |
| `UMA_RETENTION_DAYS` | `-retention-days` | `31` | Days of daily activity detail to keep (min 31, required for MAU) |

Generate an admin key:

```bash
openssl rand -hex 32
```

## API

### Report a heartbeat (no key needed)

```
POST /v1/heartbeat
Content-Type: application/json

{
  "app_id": "my-app",                                   // required, must be in the instance's UMA_ALLOWED_APPS
  "device_id": "3f1c2b4a-1d2e-4f60-8a9b-0c1d2e3f4a5b",  // required, UUID (8-4-4-4-12 format)
  "version": "1.2.0",                                   // optional, ≤64 chars
  "platform": "windows"                                 // optional, ≤64 chars
}
```

| Status | Meaning |
| --- | --- |
| 204 | Success (no body) |
| 400 | Invalid JSON / device_id is not a UUID / field too long |
| 403 | app_id not registered |
| 413 | Request body larger than 4 KB |
| 429 | Rate limited (with `Retry-After`) |

### Current stats (key required only if an admin key is set)

```
GET /v1/stats?app_id=my-app
Authorization: Bearer <UMA_ADMIN_KEY>

→ 200
{"app_id":"my-app","day":"2026-11-25","dau":1234,"wau":5678,"mau":9012,"total":20000}
```

- `dau`: distinct devices active today (UTC)
- `wau`: distinct devices active in the last 7 days (including today)
- `mau`: distinct devices active in the last 30 days (including today)
- `total`: all devices ever seen

### History (key required only if an admin key is set)

```
GET /v1/stats/history?app_id=my-app&days=30
Authorization: Bearer <UMA_ADMIN_KEY>

→ 200
{"app_id":"my-app","days":[{"app_id":"my-app","day":"2026-11-24","dau":...,"wau":...,"mau":...,"total":...}, ...]}
```

`days` defaults to 30, range 1–3650. Summaries are computed hourly (configurable): yesterday's numbers are finalized at the first rollup after midnight UTC; today's numbers are provisional and get overwritten by later rollups.

### List registered apps (key required only if an admin key is set)

```
GET /v1/apps
Authorization: Bearer <UMA_ADMIN_KEY>

→ 200 {"apps":["my-app","other-app"]}
```

### Auth rules

- Instance has `UMA_ADMIN_KEY` set: the three query endpoints above require a correct `Authorization: Bearer <key>`, otherwise 401
- Instance has no key (or an empty one): the three query endpoints need no key, and any `Authorization` header is ignored
- The heartbeat endpoint and the health check never need a key

### Health check

```
GET /healthz → 200 {"status":"ok"}
```

## Client integration

### Principles

1. On **first launch**, generate a UUID v4 locally and persist it (config file / local storage); reuse it forever. UUID v4 is random and generated offline, no network check needed.
2. Report once **at startup**, then **every few hours** (4–6 hours recommended). Multiple reports in one day count as a single active day.
3. Ignore failures; don't hammer with retries, just report at the next interval. Never block the app's main flow.
4. To let users "reset their stats identity", delete the stored UUID and generate a new one.
5. `app_id` must be registered in the instance's `UMA_ALLOWED_APPS`; to count platforms separately, use one app_id per platform (e.g. `my-app-desktop`, `my-app-android`).

### Browser / Electron / web (JavaScript)

```js
const UMA_URL = "https://uma.example.com/v1/heartbeat";
const APP_ID = "my-app";
const KEY = "uma_device_id";

function getDeviceId() {
  let id = localStorage.getItem(KEY); // in an Electron main process, store it in a file under userData instead
  if (!id) {
    id = crypto.randomUUID(); // UUID v4, random, offline
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
    // ignore, report again next time
  }
}

sendHeartbeat();                              // at startup
setInterval(sendHeartbeat, 4 * 60 * 60 * 1000); // every 4 hours
```

> When a web page on another origin reports, the instance must allow that page's origin in `UMA_CORS_ORIGINS`.

### Go client

```go
id := loadOrCreateID() // first run: uuid.NewString() (github.com/google/uuid), saved to a local config file
body, _ := json.Marshal(map[string]string{
	"app_id": "my-app", "device_id": id, "version": version, "platform": runtime.GOOS,
})
resp, err := http.Post("https://uma.example.com/v1/heartbeat", "application/json", bytes.NewReader(body))
if err == nil {
	resp.Body.Close()
}
```

### Rust (desktop) and Kotlin (Android)

> There are **no** Rust or Kotlin examples in the repository yet. Integrating only requires implementing the raw request below yourself (usually 20–30 lines):
>
> - Request: `POST https://<instance-host>/v1/heartbeat`, `Content-Type: application/json`, no key needed
> - Body: `{"app_id": "...", "device_id": "<UUID v4>", "version": "...", "platform": "..."}`
>   - `app_id` and `device_id` are required; `version` and `platform` are optional (≤64 chars each); whole body ≤4 KB
> - Response: `204` success; `400` invalid JSON / bad UUID / field too long; `403` app_id not registered; `429` rate limited (try later)
> - Device ID: generate a UUID v4 on first run and persist it (Rust: the `uuid` crate, stored in the config directory; Android: `java.util.UUID.randomUUID()` stored in SharedPreferences / DataStore), then reuse it
> - Timing: report at startup and every few hours while running; ignore failures; run it on a background thread / coroutine so it never blocks the main flow

### curl test

```bash
curl -i -X POST http://localhost:8080/v1/heartbeat \
  -H 'Content-Type: application/json' \
  -d '{"app_id":"my-app","device_id":"'"$(uuidgen | tr A-Z a-z)"'","platform":"linux"}'

curl -s 'http://localhost:8080/v1/stats?app_id=my-app' \
  -H "Authorization: Bearer $UMA_ADMIN_KEY"   # omit this line if the instance has no key
```

## Running and deploying

Requires Go 1.26+.

### Run locally

```bash
go test ./...
CGO_ENABLED=0 go build -o superlight-uma ./cmd/superlight-uma

UMA_ALLOWED_APPS=my-app \
UMA_ADMIN_KEY=$(openssl rand -hex 32) \
UMA_DB_PATH=./uma.db \
./superlight-uma
```

### Direct deployment (systemd, recommended for small servers)

`deploy/` is a ready-to-use systemd deployment; instance values are passed to `install.sh` via environment variables:

| File | Description |
| --- | --- |
| `deploy/superlight-uma.service` | systemd service: dedicated `superlight-uma` user, `Restart=on-failure`, `MemoryMax=64M`, `ProtectSystem=strict` and other hardening |
| `deploy/superlight-uma.env.example` | Example instance config; installed at `/etc/superlight-uma/superlight-uma.env` (0640, root:superlight-uma) |
| `deploy/Caddyfile.snippet` | Site config template appended to `/etc/caddy/Caddyfile` (`uma.example.com` is replaced with `UMA_DOMAIN` at install) |
| `deploy/install.sh` | Install / upgrade script |

Server layout: binary `/opt/superlight-uma/superlight-uma` (the previous version is kept as `superlight-uma.prev` on upgrade), data `/var/lib/superlight-uma/uma.db`, listening on `127.0.0.1:8082`, HTTPS provided by Caddy.

```bash
# Build on your dev machine (static binary)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -X main.version=$(git rev-parse --short HEAD)" \
  -o superlight-uma ./cmd/superlight-uma

# Upload and install (first install generates an admin key on the server with openssl and writes it to the instance config without printing it)
scp -r superlight-uma deploy <user>@<server>:/tmp/uma/
ssh <user>@<server> 'sudo UMA_DOMAIN=uma.example.com UMA_APPS=my-app /tmp/uma/deploy/install.sh /tmp/uma/superlight-uma --caddy'
```

| `install.sh` env var | Default | Purpose |
| --- | --- | --- |
| `UMA_DOMAIN` | (none, required with `--caddy`) | Caddy site domain |
| `UMA_APPS` | `my-app` | Written as `UMA_ALLOWED_APPS` on first install |
| `UMA_CORS` | `https://*.pages.dev,http://localhost:8000` | Written when the instance config lacks `UMA_CORS_ORIGINS` |
| `UMA_PORT` | `8082` | Local listen port on first install (`UMA_ADDR=127.0.0.1:<port>`) |

These variables are only written on **first install** (or when a setting is missing); later upgrades never overwrite existing config, so the server's env file is always the source of truth for an instance.

- Upgrade: repeat the two steps above. Upgrades never overwrite the existing instance config; they only add settings introduced by the new version. `--caddy` does not append again when the site block already exists.
- Roll back: `sudo cp -a /opt/superlight-uma/superlight-uma.prev /opt/superlight-uma/superlight-uma && sudo systemctl restart superlight-uma`
- View the admin key: `sudo grep UMA_ADMIN_KEY /etc/superlight-uma/superlight-uma.env`
- Change the instance config: edit the env file above, then `sudo systemctl restart superlight-uma`
- Logs: `journalctl -u superlight-uma -f`

### Docker

```bash
docker build -t superlight-uma .
docker run -d --name uma -p 8080:8080 \
  -v uma-data:/data \
  -e UMA_ALLOWED_APPS=my-app \
  -e UMA_ADMIN_KEY=replace-with-your-key \
  superlight-uma
```

The database lives at `/data/uma.db` inside the container; always mount a volume to persist it. Instance config can also be passed with `--env-file`.

### Production notes

- Put an HTTPS reverse proxy (Caddy / Nginx) in front and set `UMA_TRUST_PROXY=true`
- SQLite needs only a single instance; back up `uma.db` regularly (e.g. `sqlite3 uma.db ".backup backup.db"`)
- Storage size: detail rows ≈ active devices × 31 days, plus one row per device; 1 million devices take a few tens of MB
- If the service is down for more than a day, daily summaries for the downtime are not back-filled (live `/v1/stats` is unaffected)

## Dashboard (web/, Cloudflare Pages)

`web/` is a static admin dashboard (plain HTML + JS + CSS, hand-drawn SVG charts, no external CDN or npm packages) that can connect to any instance:

- Login: enter the instance's API address and admin key, optionally remembered in this browser (localStorage); "Log out" clears the saved key
- Keyless mode: when the instance has no `UMA_ADMIN_KEY`, the page first probes `/v1/apps` without a key and, if that succeeds, opens the dashboard directly (the top-right button becomes "Settings"); the key field in the login box may also be left empty
- App picker: reads registered apps from `/v1/apps`, or type an app_id manually
- Today's DAU / WAU / MAU / total device cards
- Trend chart (7 / 30 / 90 / 365 days, toggle each metric, hover or tap for values) and a daily table
- Mobile-friendly, follows the system dark mode
- Error messages: wrong key (401), unregistered app, network failure / CORS not allowed, etc.

Which instance the dashboard connects to is set by `web/config.js`, generated by `web/build.sh` from environment variables (see below). The repository ships an empty config: uploading the folder without a build still works, you just enter the API address in the login box.

### Deploy to Cloudflare Pages

Option 1: connect the GitHub repository (auto-deploy on push)

1. Cloudflare dashboard → Workers & Pages → Create → Pages → Connect to Git, pick your repository (authorize Cloudflare's GitHub App)
2. Production branch: `main`
3. Framework preset: `None`; Build command: `sh web/build.sh`; Build output directory: `web`
4. Under Settings → Variables and Secrets, set `UMA_API_BASE` and `UMA_APP_ID` as in the table below, then redeploy
5. Save and Deploy to get `https://<project-name>.pages.dev`

Option 2: direct upload (no repository)

- Dashboard Create → Pages → Upload assets and drag in the `web` folder; or from the command line `npx wrangler pages deploy web --project-name uma-dashboard`
- Direct uploads do not run the build command. To preset the API address, run `UMA_API_BASE=https://uma.example.com UMA_APP_ID=my-app sh web/build.sh` locally before uploading (don't commit the generated `config.js` / `_headers`; `sh web/build.sh` restores the empty config)

### Cloudflare Pages environment variables

| Setting | Value |
| --- | --- |
| Build command | `sh web/build.sh` |
| Build output directory | `web` |

| Variable | Required | Example | Purpose |
| --- | --- | --- | --- |
| `UMA_API_BASE` | Optional | `https://uma.example.com` | The instance's API address, used as the login default; also written into the CSP `connect-src` so the page can only connect to this address. Must be `https://` (`http://localhost` allowed for local testing); may include a port and path prefix |
| `UMA_APP_ID` | Optional | `my-app` | App opened by default (must be in the instance's `UMA_ALLOWED_APPS`) |

- `build.sh` is a dependency-free POSIX shell script (no npm); it validates the variables and fails the build on invalid values
- It generates `web/config.js` (`window.UMA_CONFIG = {apiBase, appId}`, loaded as a same-origin script so the CSP stays `script-src 'self'`) and `web/_headers` (from `web/_headers.template`)
- Without `UMA_API_BASE`: enter the API address at login; the CSP `connect-src` is relaxed to `'self' https: http://localhost:* http://127.0.0.1:*`, allowing any https API, which is less strict than locking to one address
- Without `UMA_APP_ID`: the first app returned by `/v1/apps` is selected
- After changing variables, redeploy in Pages for them to take effect

Other static hosts (Nginx, GitHub Pages, object storage, etc.) can serve the `web/` directory as well; `_headers` is Cloudflare Pages' format, so set equivalent security headers yourself on other platforms.

### CORS and security

- The instance's `UMA_CORS_ORIGINS` must include the dashboard's URL, e.g. `https://*.pages.dev,http://localhost:8000` (`*.pages.dev` means any pages.dev subdomain can make requests; with an admin key set, they still can't see data without the key).
- After binding a custom domain (e.g. `stats.example.com`), add it to the instance's `UMA_CORS_ORIGINS` (comma-separated) and restart. Once the domain is settled you can drop `https://*.pages.dev` to tighten access.
- `web/_headers` sets CSP and other security headers. With `UMA_API_BASE` set, `connect-src` only allows that API address; if the instance moves, change the variable and redeploy.
- Consider putting a login in front of the dashboard domain with Cloudflare Zero Trust (Access, free), so the dashboard itself isn't public either.

### Local preview

```bash
cd web && python3 -m http.server 8000
# open http://localhost:8000 (the instance's UMA_CORS_ORIGINS must include http://localhost:8000)
```

## Directory layout

```
cmd/superlight-uma/      entry point (config, HTTP server, graceful shutdown, scheduled rollup)
internal/config/         env var / flag parsing and validation
internal/server/         HTTP endpoints, auth, rate limiting, CORS, daily rollup and pruning
internal/store/          storage interface
internal/store/sqlite/   SQLite implementation
deploy/                  systemd / Caddy / install script (generic template; instance values passed at install)
web/                     static dashboard (Cloudflare Pages)
README.md                English documentation
README.zh-CN.md          Chinese documentation (简体中文)
```

## License

[MIT](LICENSE)
