# gemini-web2api

<p align="center">
  <img src="https://raw.githubusercontent.com/Sophomoresty/gemini-web2api/main/logo.png" width="200" alt="gemini-web2api logo">
</p>

[English](README.md) | [中文文档](README.zh-CN.md)

High-performance Go proxy converting Google Gemini's web interface into an OpenAI-compatible API. Zero cost, cross-platform, single static binary.

This is a complete Go rewrite of the original [Python gemini-web2api](https://github.com/Sophomoresty/gemini-web2api). It features massive concurrency throughput, minimal memory footprint, and enhanced capabilities such as **TLS Impersonation** for WAF bypass.

## Key Features

- **Optional API Keys**: No auth when `api_keys` is empty, OpenAI-style Bearer auth when configured.
- **OpenAI Compatible**: Drop-in replacement for `/v1/chat/completions`, `/v1/models`, and `/v1/responses` (Codex CLI).
- **Multimodal (Vision)**: Fully supports sending images (`image_url` / base64) to Gemini using Scotty Resumable Upload protocol natively with built-in image compression.
- **TLS Impersonation**: Built-in support to bypass Cloudflare/WAF by mimicking Chrome/Edge TLS fingerprints using `tls-client` 🔥.
- **Tool Calling**: Full function calling support (OpenAI format).
- **Multiple Models**: Flash (3.6), Extended Thinking (20k+ char output), Pro, Auto, Lite.
- **Thinking Depth**: Adjustable reasoning via `@think=N` suffix (0=deepest, 4=shallowest).
- **Web Search**: Built-in internet access (Gemini's native search).
- **High Performance**: Pure Go static binary with high concurrent SSE streaming throughput and minimal memory footprint.
- **Cross-Platform**: Zero runtime dependencies, single static binary.
- **Codex CLI**: Responses API (`/v1/responses`) for OpenAI Codex integration.
- **Gemini CLI**: Google native API (`/v1beta/models`) for Gemini CLI compatibility.

## Quick Start

### 1. Download Pre-built Binary (Recommended)

Download the pre-compiled binary for your OS (Linux, macOS, Windows) from the [Releases](https://github.com/Hau04/gemini-web2api/releases) page:

**Linux / macOS:**

```bash
chmod +x gemini-web2api
./gemini-web2api --port 8081
```

**Windows (PowerShell / Command Prompt):**

```powershell
.\gemini-web2api.exe --port 8081
```

### 2. Install via Go Toolchain

If you have Go 1.22+ installed on your system:

```bash
go install github.com/Hau04/gemini-web2api@latest
```

This compiles and places the binary directly into your `$GOPATH/bin` (or `%USERPROFILE%\go\bin`). Run it from anywhere:

```bash
gemini-web2api --port 8081
```

### 3. Build from Source

Prerequisites: Go 1.22+

```bash
git clone https://github.com/Hau04/gemini-web2api.git
cd gemini-web2api
```

After building, run the executable:

**Linux / macOS:**

```bash
go build -o gemini-web2api .
./gemini-web2api --port 8081
```

**Windows (PowerShell / Command Prompt):**

```powershell
go build -o gemini-web2api.exe .
.\gemini-web2api.exe --port 8081
```

Server starts at `http://localhost:8081/v1`.

### 4. Run via Docker

Before running via Docker, copy the example config file:

```bash
cp config.example.json config.json
```

#### Option A: Run pre-built GHCR Image (No git clone needed)

```bash
docker run -d --name gemini-web2api -p 8081:8081 -v ./config.json:/app/config.json ghcr.io/hau04/gemini-web2api:latest
```

*(If you do not have a `config.json`, simply omit `-v ./config.json:/app/config.json` to run with default settings).*

#### Option B: Build and run locally

```bash
docker build -t gemini-web2api .
docker run -d --name gemini-web2api -p 8081:8081 -v ./config.json:/app/config.json gemini-web2api
```

#### Option C: Docker Compose

```bash
docker compose up -d
```

> **Note**: The compose file mounts `./data:/app/data` for persistent cookie storage. Set `"cookie_file": "/app/data/cookie.json"` in your `config.json` so extension-synced cookies survive container restarts. (Mounting `./cookie.json` directly also works but Docker creates a *directory* if the file doesn't exist yet — the data-dir mount avoids that footgun.)
>
> **Note**: If you get empty responses (`content: null`) with Docker's default bridge network, switch to host networking: `docker run --network host ...` or add `network_mode: host` in your compose file. This is caused by Gemini's upstream rejecting requests from certain Docker NAT IP ranges.

### 5. Remote NAS / Cloudflare Tunnel Setup (Cookie-Sync Mode)

Run the server on your NAS, exposed through Cloudflare Tunnel (HTTPS), and let the browser extension auto-sync cookies to it — then you can close the browser entirely:

**Server `config.json` on the NAS:**

```json
{
  "api_keys": ["sk-your-secret-api-key"],
  "cookie_file": "/app/data/cookie.json",
  "bridge_token": "pick-a-long-random-token",
  "notify_webhook": "https://ntfy.sh/your-unique-topic",
  "notify_format": "ntfy"
}
```

- **`bridge_token`**: shared secret protecting all `/bridge/*` endpoints (cookie push, status, frozen reporting). **Required** when the port is reachable from the internet — without it anyone can overwrite your session cookies.
- **`api_keys`**: protects `/v1/*` API endpoints as usual.
- **`notify_webhook`**: when the session cookies expire (Google rejects them), the server POSTs an alert here once per expiry. Use a free [ntfy.sh](https://ntfy.sh) topic (subscribe on your phone), a Discord webhook (`"notify_format": "discord"`), Slack (`"slack"`), or any generic JSON webhook (`"generic"`).

**Extension popup → Server Connection:**

1. **Server address**: your tunnel URL, e.g. `https://gemini.example.com` (trailing `/v1` is handled automatically)
2. **Bridge token**: the same `bridge_token` value as the server config
3. Click **Save & Sync Now** — the extension immediately pushes cookies and verifies the connection.

**Daily flow:**

1. Open `gemini.google.com` once → cookies auto-push within seconds (or click Save & Sync Now)
2. Close the browser — the NAS keeps serving via direct cookie mode
3. Days later, when the XSRF token expires, you get a push notification → open Gemini once → re-sync happens automatically

> **Security**: Cloudflare Tunnel already gives you HTTPS. The `bridge_token` adds application-level auth so the tunnel URL alone isn't enough to push cookies. Keep both set.

## Client Configuration

### Cherry Studio / ChatBox / any OpenAI client

| Field    | Value                                                               |
| -------- | ------------------------------------------------------------------- |
| Base URL | `http://localhost:8081/v1`                                          |
| API Key  | any `api_keys` value from `config.json`; anything if not configured |
| Model    | `gemini-3.5-flash-thinking`                                         |

### curl

#### bash / macOS / Linux

```bash
curl http://localhost:8081/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-key" \
  -d '{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"Hello!"}]}'
```

#### PowerShell (Windows)

```powershell
curl.exe --% http://127.0.0.1:8081/v1/chat/completions -H "Content-Type: application/json" -H "Authorization: Bearer sk-your-key" -d "{\"model\":\"gemini-3.5-flash\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello!\"}]}"
```

> Note: On Windows PowerShell, use `curl.exe` and `--%` so PowerShell does not reinterpret JSON quoting or curl options.

### OpenAI Python SDK

```python
from openai import OpenAI
client = OpenAI(base_url="http://localhost:8081/v1", api_key="sk-your-key")
resp = client.chat.completions.create(
    model="gemini-3.5-flash-thinking",
    messages=[{"role": "user", "content": "Explain quantum computing"}]
)
print(resp.choices[0].message.content)
```

### Gemini CLI

```bash
export GEMINI_API_KEY=none
export GOOGLE_GEMINI_BASE_URL=http://localhost:8081
gemini
```

Supports Google native API endpoints:

- `GET /v1beta/models` — list models
- `POST /v1beta/models/{model}:generateContent` — non-streaming
- `POST /v1beta/models/{model}:streamGenerateContent` — streaming (SSE)

## Available Models

| Model                            | Description                                  | Output          |
| -------------------------------- | -------------------------------------------- | --------------- |
| `gemini-4-argon`                 | Frontier model for complex tasks (needs Pro) | **~20k+ chars** |
| `argon`, `gemini-4`              | Aliases for gemini-4-argon                   | **~20k+ chars** |
| `gemini-4-hydrogen`              | High-speed frontier execution model          | **~20k+ chars** |
| `hydrogen`                       | Alias for gemini-4-hydrogen                  | **~20k+ chars** |
| `gemini-3.8-flash`               | Latest all-around model (Gemini 3.8 Flash)   | ~12k chars      |
| `gemini-3.7-flash`               | All-around model (Gemini 3.7 Flash)          | ~12k chars      |
| `gemini-3.6-flash`               | All-around model (Gemini 3.6 Flash)          | ~12k chars      |
| `gemini-3.5-flash`               | Alias for gemini-3.6-flash                   | ~12k chars      |
| `gemini-3.5-flash-thinking`      | Extended thinking, longest output            | **~20k chars**  |
| `gemini-3.5-flash-thinking-lite` | Adaptive thinking depth                      | ~15k chars      |
| `gemini-3.1-pro`                 | Advanced math & code (needs Pro cookie)      | ~12k chars      |
| `gemini-2.5-pro`                 | Gemini 2.5 Pro                               | ~12k chars      |
| `gemini-auto`                    | Auto model selection                         | varies          |
| `gemini-flash-lite`              | Fastest answers, lightweight                 | ~10k chars      |

### Thinking Depth

Append `@think=N` to any model name to control reasoning logic (corresponds to the web UI Thinking level submenu):

```text
gemini-4-argon@think=0              # High (deepest reasoning, default for thinking models)
gemini-4-argon@think=2              # Medium
gemini-4-argon@think=4              # Low (default for standard models)
```

### Chat Session Mode (New vs Continue Chat)

In **bridge mode**, each request either starts a fresh chat in the open Gemini tab or appends to the chat currently open there. Three ways to control it, in priority order:

1. **Per-request model suffix** — append `@new` (or `@fresh`/`@newchat`) to force a fresh chat, `@continue` (or `@cont`) to keep appending to the open chat. Works on **any** model name or alias, and combines with `@think=N` in any order:
   ```text
   gemini-4-hydrogen@new                # this request starts a brand-new chat
   gemini-4-carbon@continue             # keep the open chat (incl. Gemini memory)
   gemini-4-carbon@think=0@continue     # deep thinking, keep the open chat
   carbon@new                           # aliases work too
   ```
2. **Extension popup "Chat Mode" selector** — *Auto (recommended)* / *Always new chat* / *Continue open chat*. The choice is pushed to the server (`POST /bridge/chatmode`) and persists until changed. With *Continue open chat*, click any old chat in the Gemini history sidebar first — API turns then keep adding to that conversation.
3. **Auto heuristic (default)** — a brand-new client conversation (e.g. WorkBuddy "New Chat") starts a fresh browser chat; follow-up turns continue it.

### Model Inspection Headers

Every API response (both JSON and streaming SSE) includes HTTP response headers indicating actual model routing:
- `X-Resolved-Model`: The backend model identifier actually routed to (e.g. `gemini-4-argon`).
- `X-Model-Fallback: true`: Included whenever an unmapped model was requested and fell back to the default model.

## Advanced Features (Go Exclusive)

### 1. Dynamic Model Discovery & Upstream Routing Headers

When authenticated with account cookies, the proxy dynamically queries Gemini's `otAQ7b` RPC during startup to discover available models and account tier capacity. It attaches the required upstream routing headers (`x-goog-ext-525001261-jspb`) matching web application behavior, allowing models like Gemini 4 Argon to route properly.

Discovered models are matched **by display name** (e.g. `gemini-4-carbon` → the discovered "4 Carbon" entry), so every model in the account's picker gets its own true upstream ID — argon/carbon/hydrogen no longer share one static header. In **bridge mode** the extension selects the exact entry in the web model dropdown (including versioned entries like `3.8 Flash` vs `3.5 Flash` and `2.5 Pro`).

### 2. Vision / Multimodal Uploads

Unlike the Python version which ignores images, this Go port actively intercepts `image_url` payloads (both base64 encoded and external HTTP URLs).
It initiates a background Scotty Resumable Upload session, converts the base64 image into Google internal WIZ blobs, and passes the WIZ image references directly to Gemini.
Just send OpenAI standard multimodal payloads and it will work!

### 3. TLS Impersonation (Bypass WAF)

If you run this tool in datacenters (like AWS/DigitalOcean) where Google blocks the IP with a 403 Forbidden, you can impersonate a real browser's TLS signature:

```bash
./gemini-web2api --impersonate chrome_120
```

Or in `config.json`: `"impersonate": "chrome_120"`. Supported profiles include `chrome_112` to `chrome_130`, `edge`, and `safari`.

## Optional: Cookie for Pro and Argon

Anonymous access works for Flash models, but `gemini-4-argon` and `gemini-3.1-pro` require an authenticated **Google AI Pro / Advanced** account session. Without authentication, upstream routes requests to Flash.

```bash
./gemini-web2api --cookie-file gemini-auth.json
```

### How to get cookies

1. Open Chrome, go to [gemini.google.com](https://gemini.google.com) and sign in with a **Gemini Advanced** Google account
2. Open DevTools (F12) → Application → Cookies → `https://gemini.google.com`
3. Copy these cookie values: `SID`, `HSID`, `SSID`, `APISID`, `SAPISID`, `__Secure-1PSID`
4. Create `cookie.txt` in this format:

```text
SID=your_sid; HSID=your_hsid; SSID=your_ssid; APISID=your_apisid; SAPISID=your_sapisid; __Secure-1PSID=your_1psid
```

Or use JSON format if you know your specific `SAPISID` hashing needs:

```json
{ "cookie": "SID=xxx; HSID=xxx; ...", "sapisid": "your_sapisid_value" }
```

**Alternative (browser extension — automatic)**:
- **Built-in Extension**: Use the included browser extension in the `extension/` directory! Follow [`extension/README.md`](extension/README.md) to load it unpacked in Chrome/Edge, open Gemini Web or Gemini Enterprise, and click "Download cookie.json" with 1 click.
- **Auto Cookie Sync (v1.4.0+)**: The extension now includes a background service worker that automatically pushes your Google session cookies to `localhost:8081` every 2 minutes, on tab updates, and on cookie changes. This powers the **Direct Cookie Mode fallback** — when Chrome freezes your Gemini tab (Memory Saver) or the bridge stalls, the server automatically switches to direct RPC mode using your synced cookies, no manual intervention needed. Check the extension popup's "Cookie Sync (Direct Mode)" indicator to verify it's active.
- **3rd-Party Extensions**: Or use "Cookie-Editor" from the Chrome Web Store / Firefox Add-ons, export as Header String, and save to `cookie.txt`.

### Authenticated account path and XSRF token

If the signed-in Gemini page URL contains an account index, such as:

```text
https://gemini.google.com/u/1/app/...
```

set `auth_user` to that index. Authenticated web requests may also require the page XSRF token. In the rendered Gemini page source, this token is exposed as `SNlM0e`; pass it as `xsrf_token` in `config.json`. The server sends it as the `at` form field.

Example:

```json
{
  "cookie_file": "/app/cookie.txt",
  "auth_user": "1",
  "xsrf_token": "AOOh0P...",
  "gemini_bl": "boq_assistant-bard-web-server_YYYYMMDD.xx_p0"
}
```

If authenticated requests return HTTP 400 with an `xsrf` error, refresh Gemini Web, update `xsrf_token`, and make sure `auth_user` matches the `/u/<index>/` part of the browser URL.

Pro routing requires **Gemini Advanced** (paid subscription). A free Google account cookie will authenticate but silently fall back to Flash.

### 🛡️ Anti-Ban Live Web Bridge (Recommended for Enterprise & Web)

To completely eliminate the risk of Google account or cookie bans, you do not need to export cookies. You can run `gemini-web2api` alongside an open browser tab:

1. Load the unpacked extension from [`extension/`](extension/) into Chrome or Edge (`chrome://extensions` → Developer mode → Load unpacked).
2. Keep your **Gemini Enterprise** (`vertexaisearch.cloud.google.com`) or **Gemini Web** (`gemini.google.com`) tab open.
3. Start `gemini-web2api`:
   ```bash
   ./gemini-web2api --port 8081
   ```
4. A floating status badge will appear on your Gemini web page: `🟢 Web2API Bridge: Connected (:8081)`.
5. Any request sent to `http://localhost:8081/v1/chat/completions` (from WorkBuddy, Cursor, or curl) is automatically routed through your live browser tab, streaming tokens straight from Google's web UI.

#### Direct PDF / Long-Code File Upload & Anti-Ban Protections (Bridge Mode)

When running with the Live Web Bridge, `gemini-web2api` includes built-in protections against repetitive agent tool loops and Google frequency bans:

- **Direct File, PDF & Long-Code Upload**: OpenAI `file` / `input_file` / `document` / `image_url` parts are uploaded straight into your open Gemini tab (no cookies required). For direct `localhost` agent requests (such as WorkBuddy), referenced local PDFs, Office documents, images, and **long source code files (`>= bridge_large_code_upload_bytes`, default 12 KB)** are automatically attached verbatim (up to 25 MB, max 5 files/turn) and automatically refreshed only when edited on disk.
- **Oversized Prompt / Tool-Result Spillover (`bridge_spillover_chars: 18000`)**: Prompts or huge `read_file` tool outputs exceeding 18 KB are automatically uploaded as a `.txt` attachment instead of being slammed into the web input box, preventing UI truncation and repetitive re-reads. **When session cookies are synced and healthy, oversized prompts skip the browser entirely** and are served by direct cookie mode (the full prompt goes inline into the batchexecute body — no browser file upload, which stalls on some Gemini Enterprise frontends). Only requests whose latest user message references local files that **actually exist on the server's disk** stay on the bridge so the files can be attached — merely mentioning a path in the prompt (as coding agents constantly do) never pins a request to the bridge.
- **Delta-Turn Context (`bridge_delta_context: true`)**: Follow-up turns in the same open Gemini tab send only the newly appended user/tool messages rather than re-pasting the full conversation history and tool schemas every turn (auto-resets on refresh or New Chat).
- **Frozen/Stuck Tab Failover (`bridge_no_content_timeout_sec: 120`)**: If Chrome freezes the tab mid-turn (Memory Saver) the in-flight request is aborted instantly and retried via direct cookie mode — instead of hanging until your client times out. Turns that produce only keepalives (no content) for longer than this limit are also failed over; a tab actively generating (`"generating"` phase) is never cut off by this cap. Set to `0` to disable.
- **Unclaimed-Request Failover (`bridge_pickup_timeout_sec: 45`)**: If a queued request is not picked up by any browser tab within this window — the signature of a stalled extension poll loop or a sleeping Chrome MV3 service worker (tab heartbeats but never polls) — the request is failed fast and retried via direct cookie mode. Once a tab claims the request, this timer hands off to the no-content watchdog. Set to `0` to disable.
- **Adaptive Anti-Ban Pacing & 429 Circuit Breaker**: Combines base cooldown (`bridge_min_interval_ms: 2500`), random human-like jitter (`bridge_jitter_ms: 1500`), progressive burst scaling during heavy coding loops, a rolling 10-minute window cap (`bridge_max_per_10min: 18`), and an automatic exponential cool-off circuit breaker (returning HTTP `429` + `Retry-After`) if Google signals rate limiting.

All options can be tuned in `config.json`:

```json
{
  "bridge_min_interval_ms": 2500,
  "bridge_jitter_ms": 1500,
  "bridge_max_per_10min": 18,
  "bridge_auto_upload_local_files": true,
  "bridge_large_code_upload_bytes": 12288,
  "bridge_spillover_chars": 18000,
  "bridge_delta_context": true,
  "bridge_no_content_timeout_sec": 120,
  "bridge_pickup_timeout_sec": 45
}
```

### Gemini Enterprise (Vertex AI Search) Support

If your organization uses **Gemini Enterprise** hosted at `https://vertexaisearch.cloud.google.com/`, configure `base_url`:

```json
{
  "base_url": "https://vertexaisearch.cloud.google.com",
  "cookie_file": "/app/cookie.txt"
}
```

Or pass via command-line:

```bash
./gemini-web2api --base-url https://vertexaisearch.cloud.google.com --cookie-file cookie.txt
```

When `base_url` is customized, `gemini-web2api` automatically switches its `Origin`, `Referer`, `SAPISIDHASH` calculations, and RPC endpoints to match your enterprise domain.

**Browser-synced sessions (cookie mode):** XSRF tokens are origin-specific. When the extension syncs cookies, it also reports the origin of the tab it harvested from; the server persists it into the cookie file and uses it for all direct cookie-mode requests (discovery + generation + page-token fetch + BL refresh) — it takes precedence over `base_url`. So if you ever open `gemini.google.com` after configuring `vertexaisearch.cloud.google.com`, the server follows the tab, not the stale config. The `GET /bridge/cookies` status now reports both `synced_origin` and `configured_base_url`; a mismatch (`configured=gemini.google.com` + `synced=vertexaisearch...`) is exactly what an upstream HTTP 400 on every batchexecute means — either sync from the right tab or fix `base_url`.

**Bard RPC auto-detection:** some Gemini Enterprise deployments do not serve the Bard web RPC at all (batchexecute answers HTTP 404 — the app uses the Discovery Engine `streamAssist` API). The server detects this on the first probe, latches it (`BardUnsupported`), and routes everything through the **browser bridge** — the working path there — instead of retrying direct mode into a 502. The latch resets when a cookie sync arrives from a different origin (and the discovery re-probe backoff resets with it); same-origin periodic syncs keep the latch. The 404 class of errors is never retried, and `Generate`/`GenerateStream` fail fast on the latch without spending an upstream attempt. When the tab is gone **and** direct mode is unusable, the API answers `503 bridge_offline` with a message telling you to open Gemini in Chrome.

**Discovery Engine replay (Enterprise direct mode):** the extension's net-hook captures the app's own `streamAssist` request (URL, headers, body) whenever the tab generates, splices the submitted prompt out for a `__W2A_PROMPT__` marker, and pushes the template to `POST /bridge/reqtemplate` (persisted next to the cookie file). On a latched Enterprise origin the server replays that template with fresh cookies and the new prompt — so `/v1/chat/completions` works **with the browser closed**. Templates carry the captured `Authorization` (usually SAPISIDHASH); if it has expired the server retries with freshly recomputed SAPISIDHASH values and, failing that, tells you to send one prompt through the open tab to refresh the template. Check status with `GET /bridge/reqtemplate`.

**Model selection on Enterprise:** Gemini Enterprise answers with the model its assistant is configured with unless the request carries `assistGenerationConfig.modelId`. `gemini-web2api` now sends it, so a client asking for `gemini-4-hydrogen` is no longer silently downgraded to the tenant's "auto":

```jsonc
{
  "discovery_engine_models": { "gemini-4-hydrogen": "gemini-2.5-flash", "argon": "gemini-2.5-pro" },
  "discovery_engine_default_model_id": ""   // optional fallback for unmapped names
}
```

Values must be model ids your tenant accepts (look at `assistGenerationConfig.modelId` in the tab's own `widgetStreamAssist` request). Names that already look like upstream ids (`gemini-2.5-pro`, `gemini-3-pro`) are passed through untouched; local aliases (`argon`/`hydrogen`/`carbon`) are never passed through, because the tenant does not know them. Captured-template replays get the same treatment: the modelId baked into the template is rewritten.

**Oversized prompts on Enterprise skip the browser spillover:** the `.txt` spillover exists to dodge consumer Gemini Web composer limits. On a Discovery Engine origin the prompt is inlined into the `streamAssist` JSON body, so oversized requests now go straight to direct mode instead of waiting on a live tab (which is what produced `Bridge request failed before stream start: context canceled` + 502). The bridge is still used when direct mode has no transport at all.

**Widget JWT auth (this is what fixes Enterprise 502s):** the Discovery Engine widget endpoints (`widgetStreamAssist`, `widgetCreateSession`, `widgetAddContextFile`) on `vertexaisearch.cloud.google.com` deployments do **not** accept the consumer `SAPISIDHASH` header. The app mints a short-lived HS256 JWT instead:

1. `GET https://vertexaisearch.cloud.google.com/auth/getoxsrf?csesidx=<id>` with your cookies → `{"xsrfToken":"<base64url HMAC key>","keyId":"<kid>"}`
2. sign `{"iss":<origin>,"aud":"https://content-us-discoveryengine.googleapis.com","sub":"csesidx/<id>","iat","exp":+300,"nbf"}` with that key, `kid` in the header
3. call `https://content-<location>-discoveryengine.googleapis.com/v1alpha/locations/<location>/widgetCreateSession`, then `.../widgetStreamAssist` with `Authorization: Bearer <jwt>`, `Origin/Referer: <your enterprise origin>` and `additionalParams: {"token":"-","origin":"ORIGIN_UNSPECIFIED"}`

`gemini-web2api` does all of that automatically once it knows the **`csesidx`** (the conversation/config sequence id in the Gemini Enterprise tab URL). The extension harvests it during cookie sync (`?csesidx=…`), so a normal cookie sync is enough; otherwise set `discovery_engine_csesidx`. `discovery_engine_config_id` is also required — it is the widget `configId` the app sends (captured automatically when you attach a file in the tab). `discovery_engine_location` (default `us`, or derived from your endpoint) picks the regional widget host.

Without a `csesidx` the server falls back to the old cookie-authenticated `?key=` endpoint, which is exactly the path that answers `502` / `no answer text` on most tenants — the startup banner reports which transport is in use (`Widget auth: widget JWT (csesidx=…)` vs `cookie fallback`).

**File attachments with the browser closed (Enterprise):** consumer Gemini Web uploads attachments through the `content-push.googleapis.com` pipeline and references them from the Bard RPC payload. Gemini Enterprise has no such pipeline — the app attaches files to the *assistant session* instead (`widgetAddContextFile` → `fileId`, then `streamAssist` with `fileIds` + `session`), so cookie mode used to answer `this Gemini Enterprise origin needs the browser bridge for image attachments`. That gap is closed:

1. **Capture once (recommended, zero config).** With the extension loaded, attach one file in the Gemini Enterprise tab. The net-hook records the upload request and the bridge pushes a templated copy of it (file name, MIME type and Base64 content replaced by `__W2A_FILE_NAME__` / `__W2A_FILE_MIME__` / `__W2A_FILE_B64__`) to `POST /bridge/uploadtemplate`; it is persisted as `discovery_upload_template.json` next to the cookie file. From then on the server replays that request with fresh cookies for **every** attachment — the browser can stay closed. Check it with `GET /bridge/uploadtemplate` (`{"have_template":true,...}`).
2. **Configure the endpoint.** Without a captured template, set `discovery_engine_upload_endpoint` to the `widgetAddContextFile` URL from the tab's DevTools Network tab. If it is omitted, the server derives it by swapping `widgetStreamAssist` → `widgetAddContextFile` in `discovery_engine_endpoint`. `discovery_engine_session` (e.g. `collections/default_collection/engines/my-engine/sessions/-`) and `discovery_engine_config_id` pin the session/widget config; otherwise both are read from the captured request bodies.
3. **Text fallback.** If no upload path is available, text-like attachments (code, JSON, CSV, Markdown, logs — up to 200 KB each / 400 KB per request) are inlined into the prompt with explicit `--- Attached file: … ---` markers, so coding agents still see their files. Binary attachments (images, PDFs, Office documents) return an actionable error instead of being silently dropped.

Uploaded files land in the same session the replay uses, so follow-up turns keep the file context. Identical bytes are uploaded **once**: duplicates inside a request share one `fileId`, and uploads are cached by content hash for `discovery_engine_upload_cache_sec` (default 1800, `0` disables) so an agent loop that resends the same repo files every turn skips the upload round-trip entirely. Up to 4 files upload in parallel.

**Keep the tab alive:** the extension marks every Gemini tab `autoDiscardable: false` so Chrome's Memory Saver never discards it — a discarded tab silently kills the bridge poll/heartbeat loops. It also reports tabs it finds already discarded (during each cookie sync and via a 30s liveness watchdog that pings the tab's poll loop), and the content script reports its own death on page unload. On Enterprise deployments the tab is the server's only route to Gemini: keep it open in Chrome.

**Real errors when the tab is gone:** streaming responses only commit to HTTP 200 once a tab actually claims the queued work. If nothing picks the request up within `bridge_pickup_timeout_sec` (default 45s), the API answers `503 bridge_offline` with a message telling you to open/reload the Gemini tab — instead of an empty 200 stream that clients render as a stuck or blank completion.

### WorkBuddy / AI Coding Agent Integration

`gemini-web2api` works as a drop-in OpenAI-compatible provider for coding agents like **WorkBuddy**, **CodeBuddy**, Cursor, Claude Code, Aider, and Cline. Because `gemini-web2api` natively proxies OpenAI function calling (`tools` / `tool_calls`), agents can autonomously inspect and edit local files.

Configure WorkBuddy via `~/.workbuddy/models.json` (or `%USERPROFILE%\.workbuddy\models.json` on Windows):

```json
[
  {
    "id": "gemini-4-hydrogen",
    "name": "Gemini 4 Hydrogen",
    "vendor": "Custom",
    "apiKey": "sk-gemini",
    "baseURL": "http://localhost:8081/v1",
    "supportsImages": true,
    "supportsToolCall": true,
    "supportsReasoning": true,
    "useCustomProtocol": false
  }
]
```

#### Making Argon behave like Hydrogen for agent tool calls

Some Gemini 4 models (notably **Argon**) prefer Gemini's built-in **Deep Research / web tools** and ignore the injected `# Tool Use` prompt, so WorkBuddy never receives a `tool_call` to execute locally. **Hydrogen** reliably emits the expected blocks:

````text
```tool_call
{"name": "Bash", "arguments": {"command": "gh repo clone ..."}}
```
````

If you want to keep `gemini-4-argon` configured in WorkBuddy but have tool-enabled turns actually run on Hydrogen, set `tool_model` in `config.json`:

```json
{
  "tool_model": "gemini-4-hydrogen"
}
```

Only requests that declare tools (and do not use `tool_choice: "none"`) are redirected; normal chat requests still use the requested model. Per-request suffixes such as `@new`, `@continue` and `@think=N` are preserved. A response header `X-Tool-Model-Override: true` and a server log line show when the override fired; `X-Resolved-Model` reports the model actually used.

The setting can also be applied without editing the file:

```bash
./gemini-web2api --tool-model gemini-4-hydrogen
# or
GEMINI_TOOL_MODEL=gemini-4-hydrogen ./gemini-web2api
```

The injected tool prompt additionally tells the model that local files, shell commands, GitHub repos and email must be handled by emitting `tool_call` blocks, and explicitly forbids answering them with Gemini's built-in web search or Deep Research.

## Configuration

Create `config.json` in the same directory:

```json
{
  "port": 8081,
  "host": "0.0.0.0",
  "retry_attempts": 3,
  "retry_delay_sec": 2,
  "request_timeout_sec": 180,
  "gemini_bl": "boq_assistant-bard-web-server_20260716.08_p0",
  "auth_user": null,
  "xsrf_token": null,
  "default_model": "gemini-3.6-flash",
  "tool_model": "",
  "api_keys": ["sk-your-key"],
  "cookie_file": null,
  "proxy": null,
  "impersonate": null,
  "log_requests": true,
  "bridge_token": null,
  "notify_webhook": null,
  "notify_format": "ntfy"
}
```

When `api_keys` is empty `[]`, authentication is disabled. When keys are set, `/v1/*` endpoints require `Authorization: Bearer <key>`. When `bridge_token` is set, `/bridge/*` endpoints (extension cookie sync, bridge control) require the `X-Bridge-Token` header — set the same token in the extension popup's Server Connection section.

## Proxy

If you cannot access `gemini.google.com` directly, configure a proxy:

**Method 1: CLI argument**

```bash
# Linux / macOS
./gemini-web2api --proxy http://127.0.0.1:7890

# Windows (PowerShell)
.\gemini-web2api.exe --proxy http://127.0.0.1:7890
```

**Method 2: config.json**

```json
{ "proxy": "http://127.0.0.1:7890" }
```

**Method 3: Environment variable**

```bash
# Linux / macOS (bash)
export HTTPS_PROXY=http://127.0.0.1:7890
./gemini-web2api

# Windows (PowerShell)
$env:HTTPS_PROXY="http://127.0.0.1:7890"
.\gemini-web2api.exe

# Windows (Command Prompt)
set HTTPS_PROXY=http://127.0.0.1:7890
gemini-web2api.exe
```

## Limitations

- **Not real Pro/Ultra**: Without a paid subscription cookie, `gemini-3.1-pro` routes to the same Flash model. The "Pro" label is a UI preference, not a backend model switch.
- **Single-turn only**: Each request is an independent conversation. Multi-turn context is simulated by including previous messages in the prompt.
- **Rate limits**: Google may throttle high-frequency requests. The server retries automatically but sustained heavy use may be blocked.

## License

MIT License.
