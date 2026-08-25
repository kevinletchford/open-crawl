# Browser Run Local — Specification

A local, self-hosted equivalent of Cloudflare's Browser Run service. Exposes the same REST API surface using a local headless Chromium instance, with no Cloudflare account required.

---

## Table of Contents

1. [Overview](#1-overview)
2. [Architecture](#2-architecture)
3. [Common Request Parameters](#3-common-request-parameters)
4. [Quick Actions API](#4-quick-actions-api)
   - 4.1 `/screenshot`
   - 4.2 `/pdf`
   - 4.3 `/content`
   - 4.4 `/markdown`
   - 4.5 `/snapshot`
   - 4.6 `/links`
   - 4.7 `/scrape`
   - 4.8 `/json`
   - 4.9 `/crawl` (async)
5. [Browser Sessions API](#5-browser-sessions-api)
6. [Configuration](#6-configuration)
7. [Limits & Defaults](#7-limits--defaults)
8. [Storage](#8-storage)
9. [Stage 2 — Native Mac App](#9-stage-2--native-mac-app)
10. [Stage 2 — Web App](#10-stage-2--web-app)
11. [Tech Stack](#11-tech-stack)

---

## 1. Overview

**Browser Run Local** is a drop-in local replacement for Cloudflare's Browser Run API. It provides:

- A REST HTTP server running on `localhost:7600` (configurable)
- The same 9 Quick Action endpoints as Cloudflare's service
- A Chrome DevTools Protocol (CDP) session management API
- An async job queue for `/crawl` operations backed by SQLite
- Zero cloud dependencies — everything runs locally against Chromium

**Cloudflare service → Local equivalent mapping:**

| Cloudflare | Local |
|---|---|
| Cloudflare edge network | `localhost:7600` |
| API token auth | Optional API key (dev mode: no auth) |
| Durable Objects (session state) | SQLite |
| Workers AI (`/json` endpoint) | Local LLM via Ollama or BYO API key |
| Thousands of concurrent browsers | Configurable pool (default: 5) |

---

## 2. Architecture

```
┌────────────────────────────────────────────────────────────┐
│                   Browser Run Local                        │
│                                                            │
│  HTTP Server (:7600)                                       │
│  ┌──────────────────────────────────────────────────────┐  │
│  │  Quick Actions Router                                │  │
│  │  /screenshot  /pdf  /content  /markdown  /snapshot   │  │
│  │  /links  /scrape  /json  /crawl                      │  │
│  └──────────────────┬───────────────────────────────────┘  │
│                     │                                      │
│  ┌──────────────────▼───────────────────────────────────┐  │
│  │  Browser Pool Manager                                │  │
│  │  - Launches / recycles Chromium instances            │  │
│  │  - Enforces concurrency limits                       │  │
│  │  - Idle timeout cleanup                              │  │
│  └──────────────────┬───────────────────────────────────┘  │
│                     │                                      │
│  ┌──────────────────▼───────────────────────────────────┐  │
│  │  Chromium (via CDP / Puppeteer-compatible protocol)  │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                            │
│  ┌──────────────────────────────────────────────────────┐  │
│  │  Async Job Queue (SQLite)                            │  │
│  │  - /crawl job state machine                          │  │
│  │  - Result storage (14-day TTL)                       │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                            │
│  ┌──────────────────────────────────────────────────────┐  │
│  │  CDP Session Manager                                 │  │
│  │  - Named sessions with keep-alive                    │  │
│  │  - WebSocket proxy for external Playwright/Puppeteer │  │
│  └──────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────┘
```

### Request Lifecycle (Quick Action)

1. Client POSTs to `/v1/browser-rendering/<action>`
2. Router validates request body, returns `400` on bad input
3. Browser Pool Manager acquires a free Chromium instance (or waits up to `pool_wait_timeout`)
4. Action handler drives Chromium via CDP: navigate, wait, extract
5. Result returned synchronously; browser instance returned to pool
6. Response header `X-Browser-Ms-Used` reports Chromium time in ms

### Crawl Lifecycle (Async)

1. Client POSTs to `/v1/browser-rendering/crawl` → receives `{ "id": "<job-id>" }`
2. Worker goroutine picks up job, crawls pages breadth-first
3. Client polls `GET /v1/browser-rendering/crawl/<job-id>` for status
4. Results paginated via `GET /v1/browser-rendering/crawl/<job-id>/results?page=1`
5. Job results retained for 14 days, then purged by background cleanup

---

## 3. Common Request Parameters

These parameters are accepted by most Quick Action endpoints.

### Page Load

| Parameter | Type | Default | Description |
|---|---|---|---|
| `url` | string | — | URL to navigate to. Required unless `html` is provided. |
| `html` | string | — | Raw HTML to render. Mutually exclusive with `url`. |
| `gotoOptions.waitUntil` | string | `"load"` | One of: `"load"`, `"domcontentloaded"`, `"networkidle0"`, `"networkidle2"` |
| `gotoOptions.timeout` | number | `30000` | Navigation timeout in ms |

### Viewport

| Parameter | Type | Default | Description |
|---|---|---|---|
| `viewport.width` | number | `1920` | Browser width in px |
| `viewport.height` | number | `1080` | Browser height in px |
| `viewport.deviceScaleFactor` | number | `1` | DPR |
| `viewport.isMobile` | boolean | `false` | Emulate mobile |

### Authentication

| Parameter | Type | Description |
|---|---|---|
| `authenticate.username` | string | HTTP Basic Auth username |
| `authenticate.password` | string | HTTP Basic Auth password |
| `cookies` | array | Array of `{ name, value, domain?, path? }` |
| `setExtraHTTPHeaders` | object | Key-value headers added to every request |

### Resource Filtering

| Parameter | Type | Description |
|---|---|---|
| `rejectResourceTypes` | string[] | Block by type: `"image"`, `"stylesheet"`, `"font"`, `"media"`, `"script"`, `"xhr"`, `"fetch"`, `"websocket"`, `"other"` |
| `rejectRequestPattern` | string[] | Block URLs matching these regex patterns |
| `allowResourceTypes` | string[] | If set, only these types are allowed |
| `allowRequestPattern` | string[] | If set, only URLs matching these patterns are allowed |

### DOM Injection

| Parameter | Type | Description |
|---|---|---|
| `addScriptTag` | array | `[{ url?, content? }]` — inject `<script>` tags after load |
| `addStyleTag` | array | `[{ url?, content? }]` — inject `<style>` or `<link>` tags |
| `setJavaScriptEnabled` | boolean | Disable JS execution entirely (default: `true`) |

---

## 4. Quick Actions API

Base URL: `http://localhost:7600/v1/browser-rendering`

All endpoints:
- **Method:** `POST`
- **Content-Type:** `application/json`
- **Auth:** `Authorization: Bearer <token>` (optional in dev mode)
- **Response header:** `X-Browser-Ms-Used: <ms>`

### 4.1 `/screenshot`

Renders a page and returns a PNG or JPEG image.

**Response:** Binary image (`Content-Type: image/png` or `image/jpeg`)

**Additional parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `screenshotOptions.fullPage` | boolean | `false` | Capture full scrollable page |
| `screenshotOptions.omitBackground` | boolean | `false` | Transparent background (PNG only) |
| `screenshotOptions.quality` | number | — | JPEG quality 0–100. **Not valid with PNG — returns 400.** |
| `screenshotOptions.type` | string | `"png"` | `"png"` or `"jpeg"` |
| `screenshotOptions.clip` | object | — | `{ x, y, width, height }` — crop region |
| `screenshotOptions.captureBeyondViewport` | boolean | `false` | Capture content outside viewport |
| `selector` | string | — | CSS selector — screenshot only this element |

**Example:**
```json
{
  "url": "https://example.com",
  "viewport": { "width": 1280, "height": 800 },
  "screenshotOptions": { "fullPage": true },
  "gotoOptions": { "waitUntil": "networkidle0" }
}
```

---

### 4.2 `/pdf`

Renders a page and returns a PDF document.

**Response:** Binary PDF (`Content-Type: application/pdf`)

**Additional parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `pdfOptions.format` | string | `"Letter"` | Paper format: `"A4"`, `"Letter"`, `"A3"`, etc. |
| `pdfOptions.margin` | object | — | `{ top, right, bottom, left }` — CSS units, e.g. `"1cm"` |
| `pdfOptions.scale` | number | `1` | Scale factor (0.1–2) |
| `pdfOptions.printBackground` | boolean | `false` | Include CSS backgrounds |
| `pdfOptions.landscape` | boolean | `false` | Landscape orientation |
| `pdfOptions.pageRanges` | string | — | e.g. `"1-3, 5"` |
| `headerTemplate` | string | — | HTML for page header. Supports `<span class="pageNumber">`, `<span class="totalPages">`, `<span class="date">`, `<span class="title">` |
| `footerTemplate` | string | — | HTML for page footer. Same placeholders as header. |

**Constraints:** Request body max 50 MB.

**Example:**
```json
{
  "url": "https://example.com",
  "pdfOptions": {
    "format": "A4",
    "printBackground": true,
    "margin": { "top": "1cm", "bottom": "1cm" }
  },
  "footerTemplate": "<div style='font-size:10px'>Page <span class='pageNumber'></span> of <span class='totalPages'></span></div>"
}
```

---

### 4.3 `/content`

Returns fully rendered HTML after JavaScript execution.

**Response:**
```json
{
  "success": true,
  "result": "<html>...</html>"
}
```

**No additional parameters beyond common ones.**

**Example:**
```json
{
  "url": "https://example.com",
  "gotoOptions": { "waitUntil": "networkidle2" },
  "rejectResourceTypes": ["image", "stylesheet", "font"]
}
```

---

### 4.4 `/markdown`

Converts rendered page content to Markdown.

**Response:**
```json
{
  "success": true,
  "result": "# Example Domain\n\nThis domain is for use in illustrative examples..."
}
```

**Implementation note:** Fetch rendered HTML, then convert via a Markdown converter (e.g. `html-to-markdown`, `turndown`, or a Go equivalent). Strip `<script>`, `<style>`, navigation, and footer elements before conversion using a configurable content-extraction heuristic (similar to Mozilla Readability).

**No additional parameters beyond common ones.**

---

### 4.5 `/snapshot`

Returns both rendered HTML and a Base64-encoded screenshot in a single request.

**Response:**
```json
{
  "success": true,
  "result": {
    "content": "<html>...</html>",
    "screenshot": "<base64-encoded-png>"
  }
}
```

**Additional parameters:** Same as `/screenshot` (`screenshotOptions`, `selector`, `viewport`).

---

### 4.6 `/links`

Extracts all hyperlinks from a rendered page.

**Response:**
```json
{
  "success": true,
  "result": [
    "https://example.com/about",
    "https://example.com/contact"
  ]
}
```

**Additional parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `visibleLinksOnly` | boolean | `false` | Return only links visible in viewport |
| `excludeExternalLinks` | boolean | `false` | Strip links that go outside the source domain |

**Implementation note:** Evaluate `document.querySelectorAll('a[href]')` in page context, resolve relative URLs against the page's base URL, deduplicate.

---

### 4.7 `/scrape`

Extracts specific DOM elements by CSS selector.

**Request body:**
```json
{
  "url": "https://example.com",
  "elements": [
    { "selector": "h1" },
    { "selector": ".price" },
    { "selector": "a[href]" }
  ]
}
```

**Required:** `elements` (array) — each item must have a `selector` field.

**Response:**
```json
{
  "success": true,
  "result": {
    "elements": [
      {
        "selector": "h1",
        "results": [
          {
            "text": "Example Domain",
            "html": "<h1>Example Domain</h1>",
            "attributes": { "id": "title" },
            "width": 960,
            "height": 42,
            "top": 120,
            "left": 0
          }
        ]
      }
    ]
  }
}
```

**Implementation note:** For each selector, use `document.querySelectorAll(selector)` and call `getBoundingClientRect()` on each matched element.

---

### 4.8 `/json`

Extracts structured data from a page using an LLM.

**Request body:**

| Parameter | Type | Required | Description |
|---|---|---|---|
| `url` or `html` | string | yes | Page source |
| `prompt` | string | one of `prompt`/`response_format` | Natural language extraction instruction |
| `response_format` | object | one of `prompt`/`response_format` | JSON Schema describing the output shape |
| `custom_ai` | array | no | Override the default model |

**`custom_ai` items:**
```json
{
  "model": "anthropic/claude-sonnet-4-6",
  "authorization": "Bearer sk-ant-..."
}
```

**Default model (local):** Use Ollama with `llama3.3` (or the user-configured model) if no `custom_ai` is specified. Falls back gracefully if Ollama is not running.

**Response:**
```json
{
  "success": true,
  "result": {
    "title": "Example Domain",
    "description": "This domain is for illustrative examples."
  }
}
```

**Implementation:** Render the page to Markdown (via `/markdown` logic), build a system prompt including the Markdown content, send to the configured LLM with the user's `prompt` or `response_format` as the extraction instruction.

---

### 4.9 `/crawl` (Async)

Multi-page crawl. Returns a job ID immediately; results retrieved via polling.

#### POST `/crawl` — Initiate

**Request body:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `url` | string | required | Seed URL |
| `limit` | number | `10` | Max pages to crawl |
| `depth` | number | `100000` | Max link depth from seed |
| `render` | boolean | `true` | Execute JavaScript on each page |
| `formats` | string[] | `["markdown"]` | Output formats per page: `"html"`, `"markdown"`, `"json"` |
| `source` | string | `"all"` | Link discovery mode: `"all"`, `"links"`, `"sitemaps"` |
| `includePattern` | string[] | — | Only crawl URLs matching these glob patterns |
| `excludePattern` | string[] | — | Skip URLs matching these glob patterns |
| `crawlPurposes` | string[] | — | Declared intent: `"search"`, `"ai-input"`, `"ai-train"` — respects robots.txt Content Signals |
| `maxConcurrency` | number | `3` | Simultaneous browser tabs for crawl |
| `delayMs` | number | `500` | Delay between page fetches per domain (ms) |

**Response:**
```json
{
  "success": true,
  "result": {
    "id": "job_abc123"
  }
}
```

#### GET `/crawl/:jobId` — Status

**Response:**
```json
{
  "success": true,
  "result": {
    "id": "job_abc123",
    "status": "running",
    "pagesVisited": 5,
    "pagesLimit": 10,
    "startedAt": "2026-05-22T10:00:00Z",
    "completedAt": null
  }
}
```

**Statuses:** `queued`, `running`, `completed`, `errored`, `cancelled_by_user`, `cancelled_due_to_timeout`, `cancelled_due_to_limits`

#### GET `/crawl/:jobId/results` — Results

**Query params:** `page=1` (default), `pageSize=50` (default)

**Response:**
```json
{
  "success": true,
  "result": {
    "jobId": "job_abc123",
    "status": "completed",
    "page": 1,
    "totalPages": 3,
    "data": [
      {
        "url": "https://example.com",
        "statusCode": 200,
        "markdown": "# Example...",
        "html": "<html>...</html>",
        "json": { "title": "Example" },
        "crawledAt": "2026-05-22T10:00:01Z"
      }
    ]
  }
}
```

#### DELETE `/crawl/:jobId` — Cancel

Cancels a running job.

#### robots.txt compliance
The local crawler must:
- Fetch and parse `robots.txt` before crawling any domain
- Respect `Disallow` rules for the user-agent `*`
- Respect `Crawl-delay` — if `delayMs` is lower than `Crawl-delay`, use `Crawl-delay`
- Skip crawling if `crawlPurposes` conflicts with Content Signals in `robots.txt`

---

## 5. Browser Sessions API

Exposes named, persistent browser sessions accessible via CDP WebSocket. This allows external Puppeteer or Playwright code to connect and drive browsers without restarting them between requests.

### Session Endpoints

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/sessions` | Create a new named session |
| `GET` | `/v1/sessions` | List active sessions |
| `GET` | `/v1/sessions/:id` | Get session details and WebSocket URL |
| `DELETE` | `/v1/sessions/:id` | Close and destroy session |

### POST `/v1/sessions`

**Request:**
```json
{
  "name": "my-session",
  "keepAlive": 300000,
  "userAgent": "MyBot/1.0",
  "viewport": { "width": 1920, "height": 1080 }
}
```

**Response:**
```json
{
  "id": "sess_xyz",
  "name": "my-session",
  "wsUrl": "ws://localhost:7600/v1/sessions/sess_xyz/cdp",
  "createdAt": "2026-05-22T10:00:00Z",
  "expiresAt": "2026-05-22T10:05:00Z"
}
```

### CDP WebSocket Proxy

Clients connect to `ws://localhost:7600/v1/sessions/:id/cdp` and speak the full Chrome DevTools Protocol. This is a transparent proxy to the underlying Chromium instance.

External code example:
```javascript
import puppeteer from 'puppeteer-core';

const browser = await puppeteer.connect({
  browserWSEndpoint: 'ws://localhost:7600/v1/sessions/sess_xyz/cdp'
});
const page = await browser.newPage();
await page.goto('https://example.com');
```

### Session State Machine

```
created → active → idle → expired
              ↓
           closed (DELETE)
```

- **idle timeout:** Configurable; default 60 seconds of no CDP messages
- **keep_alive:** Each CDP message resets the idle clock; `keepAlive` sets the max session lifetime in ms
- Sessions are persisted in SQLite so they survive server restarts (up to their expiry)

---

## 6. Configuration

Configuration via `config.yaml` (or env vars which override YAML values).

```yaml
server:
  host: "127.0.0.1"
  port: 7600
  auth_token: ""         # Leave empty to disable auth in dev mode

browser:
  chromium_path: ""      # Auto-detected if empty (uses system Chromium or Playwright's bundled Chromium)
  pool_size: 5           # Max concurrent browser instances
  pool_wait_timeout: 30s # How long to wait for a free browser before returning 503
  idle_timeout: 60s      # Time before an idle browser is killed
  max_session_lifetime: 600s

crawl:
  max_concurrent_jobs: 3
  result_ttl: 336h       # 14 days
  default_delay_ms: 500

ai:
  default_provider: "ollama"  # "ollama" | "openai" | "anthropic"
  ollama_base_url: "http://localhost:11434"
  ollama_model: "llama3.3"
  openai_api_key: ""
  anthropic_api_key: ""

storage:
  db_path: "./data/browser-run.db"
  results_dir: "./data/results"

log_level: "info"        # "debug" | "info" | "warn" | "error"
```

**Environment variable overrides** (prefix `BR_`):
- `BR_PORT=7600`
- `BR_AUTH_TOKEN=secret`
- `BR_CHROMIUM_PATH=/usr/bin/chromium`
- `BR_POOL_SIZE=10`
- `BR_OLLAMA_MODEL=llama3.3`
- `BR_DB_PATH=./data/browser-run.db`

---

## 7. Limits & Defaults

These defaults mirror the Cloudflare free tier to serve as a reasonable local baseline, but all are configurable.

| Limit | Default | Config key |
|---|---|---|
| Max concurrent browsers | 5 | `browser.pool_size` |
| Browser idle timeout | 60s | `browser.idle_timeout` |
| Navigation timeout | 30s | `gotoOptions.timeout` |
| Request body max | 50 MB | — |
| Crawl: max pages per job | 100,000 | `crawl.default_limit` |
| Crawl: result TTL | 14 days | `crawl.result_ttl` |
| PDF/screenshot: max viewport | 4096×4096 | — |
| Session CDP keepalive max | 10 min | `browser.max_session_lifetime` |

**HTTP error codes:**

| Code | Meaning |
|---|---|
| `400` | Bad request body (missing required field, invalid param) |
| `401` | Missing or invalid auth token |
| `404` | Job/session not found |
| `429` | Browser pool exhausted; retry after `Retry-After` header |
| `503` | No browser available within `pool_wait_timeout` |

---

## 8. Storage

All persistent state lives in SQLite at the configured `db_path`.

### Tables

```sql
-- Crawl jobs
CREATE TABLE crawl_jobs (
  id TEXT PRIMARY KEY,
  status TEXT NOT NULL,        -- queued|running|completed|errored|cancelled_*
  seed_url TEXT NOT NULL,
  config JSON NOT NULL,        -- full request params
  pages_visited INTEGER DEFAULT 0,
  pages_limit INTEGER NOT NULL,
  started_at DATETIME,
  completed_at DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at DATETIME NOT NULL -- created_at + result_ttl
);

-- Crawl results (one row per crawled page)
CREATE TABLE crawl_results (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id TEXT NOT NULL REFERENCES crawl_jobs(id),
  url TEXT NOT NULL,
  status_code INTEGER,
  markdown TEXT,
  html TEXT,
  json_result TEXT,
  crawled_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Browser sessions
CREATE TABLE browser_sessions (
  id TEXT PRIMARY KEY,
  name TEXT,
  status TEXT NOT NULL,        -- active|idle|expired|closed
  ws_url TEXT NOT NULL,
  config JSON,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at DATETIME NOT NULL,
  last_activity_at DATETIME NOT NULL
);
```

### Background Workers

- **Janitor:** Runs every 5 minutes. Deletes expired crawl jobs/results and closed sessions.
- **Crawl worker pool:** `max_concurrent_jobs` goroutines pulling from the `crawl_jobs` queue.
- **Session watchdog:** Closes sessions that exceed `idle_timeout` or `max_session_lifetime`.

---

## 9. Stage 2 — Native Mac App

A proper native macOS app built entirely in SwiftUI. The Go server binary is bundled inside the app bundle; the SwiftUI app manages its lifecycle and provides a fully native UI for all features.

### Tech Stack

| Concern | Choice |
|---|---|
| Language | Swift 6 |
| UI framework | SwiftUI |
| Min macOS target | macOS 14 (Sonoma) — required for `MenuBarExtra` + `Observable` macro |
| Networking | `URLSession` (REST), `URLSessionWebSocketTask` (CDP sessions) |
| Subprocess management | `Foundation.Process` |
| Persistence | `UserDefaults` / `@AppStorage` for settings; reads SQLite DB path from Go server |
| Markdown rendering | `AttributedString` init with markdown, or `swift-markdown-ui` package |
| Distribution | Xcode → code-signed `.dmg`, notarized via `notarytool`, auto-update via Sparkle |

### App Structure (SwiftUI Scenes)

```swift
@main
struct BrowserRunApp: App {
    @StateObject private var serverManager = ServerManager()

    var body: some Scene {
        // 1. Main multi-window dashboard
        WindowGroup("Browser Run", id: "dashboard") {
            ContentView()
                .environmentObject(serverManager)
        }
        .windowStyle(.titleBar)
        .windowToolbarStyle(.unified)
        .defaultSize(width: 1100, height: 720)

        // 2. Persistent menu bar item
        MenuBarExtra {
            MenuBarView()
                .environmentObject(serverManager)
        } label: {
            MenuBarLabel(status: serverManager.status)
        }
        .menuBarExtraStyle(.window)   // popover-style panel

        // 3. Native Settings window (⌘,)
        Settings {
            SettingsView()
                .environmentObject(serverManager)
        }
    }
}
```

---

### ServerManager (Observable)

Owns the Go subprocess and exposes live state to all views.

```swift
@Observable
final class ServerManager: ObservableObject {
    var status: ServerStatus = .stopped   // .stopped | .starting | .running | .error(String)
    var port: Int = 7600
    var poolUsage: PoolUsage = .zero      // activeCount / poolSize
    var recentRequests: [RequestRecord] = []
    var activeCrawlJobs: [CrawlJob] = []
    var activeSessions: [BrowserSession] = []

    private var process: Process?
    private var logPipe: Pipe?
    private var statsTask: Task<Void, Never>?

    func start() async { ... }   // launches bundled binary, waits for :port/health
    func stop() async { ... }    // SIGTERM → wait 3s → SIGKILL
    func restart() async { ... }

    // Polls GET /v1/stats every 2s while running
    private func startStatsPolling() { ... }
}

enum ServerStatus {
    case stopped, starting, running, error(String)
}
```

The Go binary is bundled at `BrowserRun.app/Contents/Resources/browser-run-server`. On first launch, the app copies it to `~/Library/Application Support/BrowserRun/` so macOS can exec it without Gatekeeper blocking it from inside the bundle.

---

### Menu Bar (MenuBarExtra)

A compact popover panel (280 × 360 pt):

```
┌─────────────────────────────────┐
│  ● Browser Run    [running]     │
│  Port: 7600  ·  3/5 browsers    │
├─────────────────────────────────┤
│  [Open Dashboard]               │
│  [Stop Server]                  │
├─────────────────────────────────┤
│  Recent                         │
│  ✓ /screenshot  example.com  12ms│
│  ✓ /pdf         docs.dev     89ms│
│  ✗ /crawl       blog.io    error │
├─────────────────────────────────┤
│  [Settings]           [Quit]    │
└─────────────────────────────────┘
```

- The status dot is the `MenuBarExtra` label — green (`●`) when running, red when stopped/errored, amber during start
- Pool usage shown as `N/M browsers` with a mini progress bar
- Last 5 requests shown inline with status icon, endpoint, domain, and duration
- "Open Dashboard" focuses or creates the main `WindowGroup` window

---

### Main Dashboard Window

Uses a `NavigationSplitView` with a sidebar and a detail pane.

#### Sidebar

```
Browser Run
────────────
● Dashboard
⚡ Quick Actions
  › Screenshot
  › PDF
  › Content
  › Markdown
  › Snapshot
  › Links
  › Scrape
  › JSON
🕸 Crawl Jobs
🖥 Sessions
📋 Logs
```

#### Dashboard View

Native SwiftUI stats grid:

```
┌──────────────┐ ┌──────────────┐ ┌──────────────┐ ┌──────────────┐
│  Requests    │ │ Browser Pool │ │  Crawl Jobs  │ │  Sessions    │
│  247 today   │ │   3 / 5      │ │  2 running   │ │  1 active    │
└──────────────┘ └──────────────┘ └──────────────┘ └──────────────┘

Recent Requests
────────────────────────────────────────────────────────────────────
Endpoint     URL                    Duration   Status   Time
/screenshot  https://example.com    142 ms     ✓ 200    14:32:01
/pdf         https://docs.dev       891 ms     ✓ 200    14:31:44
/crawl       https://blog.io          —        ✗ err    14:30:10
```

#### Quick Actions View

One sub-view per endpoint, accessible from the sidebar. Example: Screenshot view:

```
┌─ Screenshot ───────────────────────────────────────────────────┐
│                                                                │
│  URL  [https://                                   ]           │
│                                                                │
│  ┌─ Options ──────────────────────────────────────────────┐   │
│  │  Full page          ○ Off  ● On                        │   │
│  │  Format             [PNG ▼]                            │   │
│  │  Viewport           1920  × 1080                       │   │
│  │  Wait until         [networkidle0 ▼]                   │   │
│  │  Timeout            30000 ms                           │   │
│  │  Reject resource types  [image] [stylesheet] [+]       │   │
│  └────────────────────────────────────────────────────────┘   │
│                                                                │
│                                      [Capture Screenshot]      │
│                                                                │
│  ┌─ Result ───────────────────────────────────────────────┐   │
│  │                                                        │   │
│  │          [image rendered here, pinch-to-zoom]          │   │
│  │                                                        │   │
│  │    X-Browser-Ms-Used: 412 ms      [Save to Disk]       │   │
│  └────────────────────────────────────────────────────────┘   │
└────────────────────────────────────────────────────────────────┘
```

Result rendering per action:
- **Screenshot** → `Image(nsImage:)` with pinch-to-zoom and "Save to Disk" (via `NSSavePanel`)
- **PDF** → `PDFView` from PDFKit, with page controls and "Save to Disk"
- **Content / Markdown (raw)** → `TextEditor` (read-only, monospaced) with syntax highlight via `AttributedString`
- **Markdown (rendered)** → `Text` with `.init(_:options:)` or `swift-markdown-ui`
- **Snapshot** → split view: image on left, HTML on right
- **Links** → `List` of URLs with "Open in Browser" buttons and external-link icon
- **Scrape** → `Table` view: Selector | Text | Attributes | Width | Height
- **JSON** → custom recursive `OutlineGroup`-based tree viewer

#### Crawl Jobs View

```
┌─ Crawl Jobs ───────────────────────────────────────────────────┐
│  [+ New Crawl]                                                  │
│                                                                 │
│  ID            Seed URL           Status     Pages    Started   │
│  job_abc123    https://example…   ● Running  42/100   14:30     │
│  job_def456    https://blog.io    ✓ Done      87/100   13:10    │
│  job_ghi789    https://docs.dev   ✗ Errored    3/10    12:55    │
│                                                                 │
│  ▶ job_abc123 (selected, expanded)                              │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  Progress  ████████████░░░░░░░░░░  42 / 100 pages        │   │
│  │                                         [Cancel]         │   │
│  │  Results                                                  │   │
│  │  URL                    Code  Crawled At   [Markdown][HTML]│  │
│  │  https://example.com    200   14:30:01     [View]  [View] │   │
│  │  https://example.com/a  200   14:30:03     [View]  [View] │   │
│  └──────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
```

Results open in a sheet showing the Markdown or HTML in a scroll view.

#### Sessions View

```
┌─ Browser Sessions ─────────────────────────────────────────────┐
│  [+ New Session]                                                │
│                                                                 │
│  Name         Status    Expires       Last Activity             │
│  my-session   ● Active  in 4m 12s     14:33:50                 │
│  test-run     ○ Idle    in 0m 48s     14:29:01                 │
│                                                                 │
│  WebSocket URL:  ws://localhost:7600/v1/sessions/sess_xyz/cdp  │
│                  [Copy]                                         │
│                                            [Close Session]      │
└─────────────────────────────────────────────────────────────────┘
```

#### Logs View

Live-streamed log tail using SSE (`URLSession` with `AsyncBytes`):

```
┌─ Logs ─────────────────────────────────────────────────────────┐
│  Filter: [All ▼]   Search: [              ]   [Clear]          │
├─────────────────────────────────────────────────────────────────┤
│  14:33:52 INFO  POST /screenshot url=https://example.com       │
│  14:33:52 DEBUG browser acquired from pool (2/5 active)        │
│  14:33:53 INFO  screenshot complete ms=412                      │
│  14:33:55 WARN  crawl job job_ghi789 page errored: 403         │
└─────────────────────────────────────────────────────────────────┘
```

Rows colour-coded: DEBUG=grey, INFO=primary, WARN=orange, ERROR=red.

---

### Settings Window (⌘,)

Uses `Form` inside a `TabView` with SF Symbol icons:

**Tab: Server**
- Port (number field, 1024–65535)
- Auth token (SecureField + toggle to enable)
- Browser pool size (Stepper, 1–20)
- Browser idle timeout (Slider + label, 10s–600s)
- Chromium path (TextField + "Auto-detect" button)

**Tab: Crawling**
- Default crawl limit (number field)
- Default delay between requests (ms)
- Max concurrent crawl jobs (Stepper)
- Result retention (days, Stepper)

**Tab: AI**
- Default provider (Picker: Ollama / OpenAI / Anthropic)
- Ollama base URL + model name
- OpenAI API key (SecureField)
- Anthropic API key (SecureField)

**Tab: General**
- Launch at login (Toggle → `SMAppService.mainApp.register()`)
- Show in Dock (Toggle — menu-bar-only mode hides from Dock via `LSUIElement`)
- Theme (Picker: System / Light / Dark)

---

### Auto-launch

```swift
import ServiceManagement

func setLaunchAtLogin(_ enabled: Bool) throws {
    if enabled {
        try SMAppService.mainApp.register()
    } else {
        try SMAppService.mainApp.unregister()
    }
}
```

---

### Bundled Server Binary

The Xcode project has a Build Phase that:
1. Runs `make build-server` (cross-compiles the Go binary for `darwin/arm64` + `darwin/amd64` and `lipo`-joins them into a universal binary)
2. Copies the output to `$(BUILT_PRODUCTS_DIR)/BrowserRun.app/Contents/Resources/browser-run-server`

On first run, `ServerManager.start()` copies the binary to `~/Library/Application Support/BrowserRun/browser-run-server`, `chmod +x`s it, and execs from there (avoids Gatekeeper issues with executing directly from inside the app bundle).

---

### Distribution

- Xcode Archive → `xcodebuild -exportArchive` → Developer ID–signed `.app`
- Wrapped in a `.dmg` via `create-dmg` (drag-to-Applications background)
- Notarized with `notarytool submit` + `stapler staple`
- Auto-update via **Sparkle 2** (`SPUStandardUpdaterController`, checks an `appcast.xml` on GitHub Releases)

---

## 10. Stage 2 — Web App

A browser-based GUI for exploring and using Browser Run Local. Runs as a React SPA served by the local HTTP server at `http://localhost:7600/ui`.

### Pages & Features

#### 1. Dashboard `/ui`
- Server status card (uptime, pool usage bar: N/5 browsers active)
- Recent requests table (last 50): endpoint, URL, duration, status
- Quick stat cards: requests today, crawl jobs running, sessions active

#### 2. Quick Actions `/ui/actions`

A form-based UI for each of the 9 endpoints. Layout:

```
┌─────────────────────────────────────────────────────────┐
│  [Screenshot] [PDF] [Content] [Markdown] [Snapshot]     │
│  [Links] [Scrape] [JSON] [Crawl]                        │
├─────────────────────────────────────────────────────────┤
│  URL: [                              ]  [Run]           │
│                                                         │
│  ┌─ Options ──────────────────────────────────────────┐ │
│  │  Full page: [ ]   Format: [PNG ▼]   Width: [1920]  │ │
│  │  Wait until: [networkidle0 ▼]   Timeout: [30000]   │ │
│  └────────────────────────────────────────────────────┘ │
│                                                         │
│  ┌─ Result ───────────────────────────────────────────┐ │
│  │  [image preview / JSON viewer / HTML viewer]       │ │
│  │                                    [Download]      │ │
│  └────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
```

- Result panel renders appropriately per action:
  - Screenshot → `<img>` preview
  - PDF → embedded `<iframe>` or download button
  - Content/HTML → syntax-highlighted code viewer
  - Markdown → rendered preview + raw toggle
  - JSON → collapsible tree viewer
  - Links → clickable list with external-link icons
  - Scrape → table of elements with selector, text, attributes columns
  - Crawl → job status dashboard (see below)

#### 3. Crawl Jobs `/ui/crawl`
- Table of all jobs: ID, seed URL, status badge, pages visited/limit, started, elapsed
- Click a job → detail view:
  - Status and progress bar
  - Cancel button (if running)
  - Results table: URL, status code, crawled at, preview buttons (Markdown / HTML / JSON)
  - Pagination
- "New Crawl" button → opens the crawl form

#### 4. Sessions `/ui/sessions`
- Active sessions list: name, created, expires, last activity, status badge
- "Create Session" button → form with name, keepAlive, viewport
- Click a session → shows WebSocket URL (copy button), CDP endpoint
- "Close" button per session

#### 5. Settings `/ui/settings`
- Reads and writes the server's config via `GET /v1/config` / `PUT /v1/config`
- Form fields for all config options (mirrors the Preferences Panel in the Mac app)
- "Restart server" button (Mac app only; web-only mode shows a "config written, restart manually" notice)

#### 6. Logs `/ui/logs`
- Live-streamed server logs via SSE (`GET /v1/logs/stream`)
- Filter by level (debug/info/warn/error) and search

### Config management endpoints (Stage 2 additions)

| Method | Path | Description |
|---|---|---|
| `GET` | `/v1/config` | Return current config as JSON |
| `PUT` | `/v1/config` | Update config (non-destructive merge); triggers graceful reload |
| `GET` | `/v1/stats` | Aggregate stats: requests today, uptime, pool usage |
| `GET` | `/v1/logs/stream` | SSE stream of log lines |

### UI Tech Stack

- **Framework:** React 18 + TypeScript
- **Build:** Vite
- **UI components:** shadcn/ui (Tailwind-based, no design system lock-in)
- **State / data fetching:** TanStack Query
- **Code highlighting:** Shiki
- **JSON viewer:** `@textea/json-viewer`
- **Routing:** React Router v6

---

## 11. Tech Stack

### Stage 1 — Local Server

| Concern | Choice | Rationale |
|---|---|---|
| Language | Go | Already used in this repo; fast, single binary |
| HTTP framework | `net/http` + `chi` router | Lightweight, no magic |
| Browser control | `chromedp` (Go CDP client) | Pure Go, no Node.js dependency |
| HTML→Markdown | `html-to-markdown` (Go) | Handles common cases well |
| SQLite | `modernc.org/sqlite` | Pure Go, no CGo required |
| Config | `viper` | YAML + env var override |
| CDP WebSocket proxy | `gorilla/websocket` | Reliable WS implementation |
| LLM (default) | Ollama HTTP API | Local-first; falls back to configured provider |

### Stage 2 — Native Mac App

| Concern | Choice |
|---|---|
| Language | Swift 6 |
| UI framework | SwiftUI (macOS 14+) |
| Menu bar | `MenuBarExtra` (SwiftUI scene) |
| PDF rendering | PDFKit (`PDFView`) |
| Markdown rendering | `swift-markdown-ui` package |
| Networking | `URLSession` + `AsyncBytes` (SSE), `URLSessionWebSocketTask` |
| Subprocess | `Foundation.Process` |
| Login item | `ServiceManagement.SMAppService` |
| Auto-update | Sparkle 2 |
| Packaging | Xcode Archive → Developer ID `.dmg` + `notarytool` notarization |

### Stage 2 — Web UI

| Concern | Choice |
|---|---|
| Framework | React 18 + TypeScript |
| Build | Vite (served as static files from Go server) |
| Components | shadcn/ui |
| Data fetching | TanStack Query |

---

## Appendix: Endpoint Summary

| Endpoint | Method | Sync/Async | Response type |
|---|---|---|---|
| `/v1/browser-rendering/screenshot` | POST | Sync | `image/png` or `image/jpeg` |
| `/v1/browser-rendering/pdf` | POST | Sync | `application/pdf` |
| `/v1/browser-rendering/content` | POST | Sync | `application/json` |
| `/v1/browser-rendering/markdown` | POST | Sync | `application/json` |
| `/v1/browser-rendering/snapshot` | POST | Sync | `application/json` |
| `/v1/browser-rendering/links` | POST | Sync | `application/json` |
| `/v1/browser-rendering/scrape` | POST | Sync | `application/json` |
| `/v1/browser-rendering/json` | POST | Sync | `application/json` |
| `/v1/browser-rendering/crawl` | POST | Async (job) | `application/json` (job ID) |
| `/v1/browser-rendering/crawl/:id` | GET | — | `application/json` (status) |
| `/v1/browser-rendering/crawl/:id/results` | GET | — | `application/json` (paginated) |
| `/v1/browser-rendering/crawl/:id` | DELETE | — | `application/json` |
| `/v1/sessions` | POST | Sync | `application/json` |
| `/v1/sessions` | GET | Sync | `application/json` |
| `/v1/sessions/:id` | GET | Sync | `application/json` |
| `/v1/sessions/:id` | DELETE | Sync | `application/json` |
| `/v1/sessions/:id/cdp` | WS | — | WebSocket (CDP protocol) |
| `/v1/config` | GET/PUT | Sync | `application/json` |
| `/v1/stats` | GET | Sync | `application/json` |
| `/v1/logs/stream` | GET | SSE | `text/event-stream` |
