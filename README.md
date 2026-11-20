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

## WorkBuddy / MCP compatibility

Built for agent runtimes that drive tools, connectors, skills and MCP servers (WorkBuddy, Claude-style agents, the OpenAI SDKs):

| Feature | Behaviour |
| --- | --- |
| `GET /v1/models` | Every alias with `capabilities` (`tools`, `connectors`, `mcp`, `vision`, `attachments`, `json_mode`, `streaming`), `context_window`, `premium` and `research_prone` flags, so a client can filter before enabling a feature |
| `GET /v1/models/{id}` | Single-model probe (200/404) used by agent runtimes to validate a configured id |
| `parallel_tool_calls: false` | Enforced in the prompt: exactly one `tool_call` block per turn |
| Duplicate / nameless tools | Merged MCP + connector sets are deduped by name; built-in declarations such as `{"type":"web_search"}` (no function name) are skipped instead of leaking a nameless entry |
| MCP tool names | Namespaced names (`mcp__server__tool`, `server.tool`) are passed through untouched — they are executed by your client, never by Gemini |
| Skills | Long `system`-only prompts are supported (skills are just instruction bundles); `/v1/responses` is fully supported alongside `/v1/chat/completions` |

### Premium models never fall into Deep Research

Neon, Hydrogen, Carbon and Argon are flagged `research_prone`, because the Gemini UI happily pushes them into Deep Research — a mode that reads the public web only and **cannot run your tools, connectors or MCP servers**. With `prevent_deep_research` (default `true`) a tool-enabled request to one of those models:

- drops upstream research/browsing tools (`google_search`, `web_search`, `web_search_preview`, `deep_research`, `url_context`, `browse`, …) while keeping your real tools;
- folds an `EXECUTION MODE` line into the `# Tool Use` preamble: Deep Research / built-in search are disabled, facts about the machine, repo, files or apps must come from a `tool_call`, and missing data must be fetched with a tool instead of researched;
- answers with `X-Deep-Research: disabled` and logs `Execution-mode guard applied for premium model "gemini-4-neon" …`.

```jsonc
{ "prevent_deep_research": true, "tool_model": "gemini-3.8-flash" }
```

`tool_model` still wins when you want tool-enabled requests to run on a cheaper, reliable tool-caller regardless of the model the client asked for.

## Troubleshooting the exact failures in a real Enterprise log

**`context-file upload rejected (HTTP 400): One or more required fields are missing.`** — the widget's `widgetAddContextFile` needs a `configId`, and without a captured upload template or `discovery_engine_config_id` the server can only derive the URL, not the field. `gemini-web2api` no longer burns the round trip: it refuses up front with

```
cannot attach "code_context.txt": the Discovery Engine widget configId is unknown and the widget rejects uploads without it —
send one message in the Gemini Enterprise tab with the extension enabled (the request is captured automatically), or set "discovery_engine_config_id"
```

and the startup banner states the same thing instead of promising a fallback. When a 400 does come back, the error now lists the fields that were actually sent (`sent fields: addContextFileRequest{fileContents, fileName, mimeType}, additionalParams, configId`), so a missing field is visible rather than guessed.

**`modelId=<none>` / "still answers as Auto"** — `discovery_engine_models` is empty, so nothing was sent. Two ways out:

1. **Automatic:** send one message in the Gemini Enterprise tab with the extension enabled. Its own `widgetStreamAssist` request is captured, and the tenant's real `assistGenerationConfig.modelId` (plus `configId` and `session`) is then reused — the log becomes
   `Enterprise: calling the Discovery Engine widgetStreamAssist API (files=0, bearer=false, requested="gemini-4-hydrogen", modelId="gemini-2.5-pro", source=captured)`
   and `/v1/model-info` reports `"source": "captured"`.
2. **Explicit:** map the alias (see the section above) for exact control.

`source=captured` means "a tenant-valid model is now being sent, but it is whatever the tab had selected" — set a mapping if you want a specific one. Previously the log printed `model="gemini-4-hydrogen"`, which implied the alias was sent upstream; it never was.

### Authenticating the diagnostic endpoints

`/v1/*` is protected by `api_keys`. Any of these forms works — `invalid api key` now tells you so in the response body:

```bash
KEY=your-key-from-config
curl -H "Authorization: Bearer $KEY" http://localhost:8081/v1/model-info?model=gemini-4-hydrogen
curl -H "x-api-key: $KEY"            http://localhost:8081/v1/model-info?model=gemini-4-hydrogen
curl "http://localhost:8081/v1/model-info?model=gemini-4-hydrogen&key=$KEY"
```

### Discovering the tenant model id (no DevTools required)

If direct mode should name the model but no tenant id is known, `gemini-web2api` now **asks the tenant instead of guessing**: one minimal `widgetStreamAssist` call carries the requested alias as `assistGenerationConfig.modelId`. If the tenant accepts it, the id is verified, cached per alias in `enterprise_models.json`, and used from then on:

```
Gemini Enterprise: tenant accepted model id "gemini-4-hydrogen" for "gemini-4-hydrogen" — cached in enterprise_models.json, direct mode will now select it
```

If the tenant rejects it, the alias is not probed again in this process and the request continues without a model id (tenant default) instead of failing:

```
Gemini Enterprise: tenant does not accept "gemini-4-neon" as a model id (the tenant rejected model id "gemini-4-neon" (HTTP 400) …) — falling back to the tenant's auto/default model
```

Control it with `discovery_engine_probe_models` (default `true`), or run the probe explicitly and see the exact upstream answer:

```bash
curl -X POST localhost:8081/v1/model-probe \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-4-hydrogen","candidate":"gemini-4-hydrogen"}'
# {"ok":true,"model":"gemini-4-hydrogen","model_id":"gemini-4-hydrogen","source":"learned",…}
```

Try candidates one at a time (`"candidate":"gemini-2.5-flash"`) until one is accepted; each success is remembered. Resolution order is: `discovery_engine_models` → tenant-id passthrough → `discovery_engine_default_model_id` → **learned (probe-verified)** → captured from the tab → tenant default.

### Routing rule that makes the picker authoritative

On a Gemini Enterprise origin there are exactly two ways to get the model you asked for:

1. **Direct mode** — `assistGenerationConfig.modelId` in the widget request. This only works when a tenant id is known (a `discovery_engine_models` mapping, a passthrough tenant id, a configured default, or the id learned from the tab's captured request).
2. **Bridge mode** — the extension clicks the model in the tab's picker. This always works for models the picker actually shows.

So routing is now model-aware: **if direct mode cannot name the requested model, the request goes through the bridge instead of being silently answered by the tenant's auto model.** That is the case that used to produce "I selected Hydrogen, it still says Auto":

```
Keeping the browser bridge: direct mode has no tenant model id for "gemini-4-hydrogen",
so only the tab's model picker can select it (map discovery_engine_models to make direct mode honour it)
```

The response carries `X-Model-Route: bridge-picker`, and `/v1/model-info` reports `"source": "bridge-picker"` with `selection_live: true`.

Two consequences worth knowing:

- **The tab must stay open and awake.** Chrome's Memory Saver freezes background tabs; a frozen tab is reported as disconnected, the bridge is unusable, and the turn falls back to direct mode → tenant auto. The log says so explicitly (`MODEL SELECTION WARNING: … no usable browser tab …`), and `/bridge/status` shows `frozen: true`.
- **Mapped aliases never touch the tab.** Once `discovery_engine_models` has the id, direct mode serves the request with no browser at all — which is also what makes attachments (`files=N`) possible.

## "I selected Hydrogen, but it answers as Auto" — diagnosing model selection

The model that answers is decided in three different places depending on the route, and every one of them can silently fall back. `GET /v1/model-info?model=gemini-4-hydrogen` reports which one applies right now:

```jsonc
{
  "requested": "gemini-4-hydrogen",
  "resolved": { "name": "gemini-4-hydrogen", "mode": 8, "think": 4, "fallback": false },
  "origin": "discovery_engine",
  "upstream": { "model_id": "", "source": "tenant-default", "selection_live": false },
  "transport": ["bridge", "direct"],
  "discovery_engine_models": {},
  "warnings": ["Gemini Enterprise model selection is INACTIVE: discovery_engine_models is empty, so every request is answered by the tenant's auto/default model and \"gemini-4-hydrogen\" is ignored. …"]
}
```

`upstream.source` tells you exactly what happened:

| `source` | Meaning |
| --- | --- |
| `mapping` | `discovery_engine_models` matched — the tenant answers with the mapped model |
| `passthrough` | the client asked for a tenant id directly (`gemini-2.5-pro`) |
| `configured-default` | alias unmapped, `discovery_engine_default_model_id` used |
| `tenant-default` | **nothing was selected — the tenant decides (usually "Auto")** |
| `alias` | consumer origin: the alias itself routes the model |
| `bridge-picker` | Enterprise without a tenant id: the request goes through the browser bridge, which selects the model in the tab's picker |
| `captured` | Enterprise without a mapping: the tenant `modelId` learned from the tab's captured request is reused |
| `(tab picker)` (warning) | a model picker is required but no tab is connected — the turn falls back to the tenant's auto model |

Every chat response carries the same verdict in headers, so you never have to believe the answer text (`Gemini`'s self-description in the reply is generated prose, not a routing report):

```
X-Resolved-Model: gemini-4-hydrogen
X-Upstream-Model: tenant-default          # or the real tenant model id
X-Upstream-Model-Source: tenant-default   # mapping | passthrough | configured-default | alias
X-Model-Selection: ignored-by-tenant      # live | ignored-by-tenant
```

**Fix for Enterprise tenants:** the alias must be mapped to a model id the tenant actually offers. Read it from the tab's own `widgetStreamAssist` request (`assistGenerationConfig.modelId`) and configure:

```jsonc
{ "discovery_engine_models": { "gemini-4-hydrogen": "<tenant model id>", "gemini-4-neon": "<tenant model id>" } }
```

If the tenant's picker does not list Hydrogen/Neon at all, no mapping can invent it — map the alias to the closest model the tenant does offer (e.g. its 4 Argon entry), or use a model id that exists there.

**Bridge mode:** the extension clicks the entry in the tab's model dropdown and then reports the outcome. When the requested model is missing from the picker you now get, instead of a silent Auto answer:

```
MODEL SELECTION WARNING: the extension could not select "gemini-4-hydrogen" in the tab (picker currently shows "Auto") — this turn and later turns are answered by whatever the tab has open …
```

and `/bridge/status` grows a `model_selection: { requested, applied, ok }` block, so the last switch attempt is always inspectable.

## Available Models

| Model                            | Description                                  | Output          |
| -------------------------------- | -------------------------------------------- | --------------- |
| `gemini-4-neon`                  | Newest frontier model for complex work (needs Enterprise/Pro) | **~20k+ chars** |
| `neon`, `gemini-4-neon-thinking` | Aliases for gemini-4-neon                    | **~20k+ chars** |
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

### 3. Image & Video Generation

Gemini web can generate images (Nano Banana) and video (Veo), but it returns them as *media*, not as chat text — the text pipeline drops binary parts, so a plain `/v1/chat/completions` call to an image model gets you a description. These two endpoints drive the media path and hand the result back in an OpenAI-shaped envelope:

```bash
# image → URL (default) or inline base64
curl http://localhost:8081/v1/images/generations \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gemini-4-hydrogen","prompt":"a red cube on a wooden table, studio light","response_format":"b64_json"}'

# video (Veo generation takes minutes — raise request_timeout_sec if needed)
curl http://localhost:8081/v1/videos/generations \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"prompt":"a wave crashing on rocks at sunset","aspect_ratio":"16:9","duration_seconds":8}'
```

```json
{"created":1732147200,"model":"gemini-4-hydrogen","kind":"image",
 "data":[{"kind":"image","mime_type":"image/png","url":"https://lh3.googleusercontent.com/gg/…","b64_json":"iVBORw0KGgo…"}]}
```

How it works:

- The prompt is wrapped so Gemini uses its own media tool and replies with the rendered asset (or with an explicit `IMAGE GENERATION UNAVAILABLE` marker instead of a plausible essay).
- Media is recognised **by content** — data URIs, file-header magic inside base64 JSON strings (PNG/JPEG/GIF/WebP/MP4/WebM), and asset URLs on Google hosts — so the same extractor works on consumer `StreamGenerate`, Discovery Engine `streamAssist`, and text-only transports alike. Nothing is fabricated: no asset means `502` with the model's own words in `error.text`.
- `response_format: "b64_json"` inlines the bytes; that only fetches from Google asset hosts (a URL scraped out of model output never becomes an arbitrary server-side request).
- Image/video model names (`nano-banana`, `veo-3`, …) are not chat models in the registry, so an unknown name is forwarded upstream with the default transport settings and resolved by the tenant's own model-id rules (`/v1/model-probe` still works for those).

### 4. TLS Impersonation (Bypass WAF)

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

**Editing a long code file without the fetch loop:** when a prompt references a source file at or above `enterprise_auto_attach_min_bytes` (default: `bridge_large_code_upload_bytes`, 8 KB), the file is attached and the prompt gets a short inventory instead of the body:

```
# Attached workspace files (already loaded — do not re-read)
- /work/app/handlers.ts — 1,842 lines, 78 KB — attached as context file "handlers.ts"
These attachments are the current on-disk version as of this request. Do not call read_file, grep or shell cat to
fetch their contents again ... Make the change with your edit/write tool using a targeted patch (str_replace /
line range) — the updated file is re-attached automatically on the next turn, so no verification read is needed.
```

That removes both halves of the round-trip cost: the model never spends a turn `read_file`-ing the file, and it never has to echo the whole file back as a tool argument (the expensive output). After WorkBuddy writes the patch, the **next** request re-reads the file from disk and attaches the new bytes as a new version, so the model sees its own edit without a verification read. Uploads are content-hash cached: one upload per file *version*, zero per turn.

Honest scope: the attachment is read-only context. The actual edit still goes through WorkBuddy's own write tool — but that direction is free, because content moves from the model to your disk, never back to Gemini.

**How images are actually read:** `widgetAddContextFile` is the same call the Gemini Enterprise app makes when you paste a picture into its composer — the bytes land in the assistant session and `streamAssist` references them by `fileId`, so the multimodal assistant reads the image itself (vision tokens), not a filename. `gemini-web2api` uploads images byte-for-byte with their real MIME type (`image/png`, `image/jpeg`, …) and never converts them to text: `IsTextLike()` returns false for image types, so an image can never be silently inlined as garbage. The upload log proves the shape: `Enterprise context file "diagram.png" uploaded as image/png (240 KB, fileId=…)`.

Images arrive by any of the three routes: OpenAI `image_url` parts (data URL or remote URL, fetched then uploaded), Google `inlineData` parts, and local paths referenced in a prompt (`.png/.jpg/.jpeg/.webp/.gif/.bmp` are auto-attached). If the tenant rejects the upload, the request fails with an actionable error instead of dropping the image.

**Anti-ban attachment strategy (big files, images, PDF/Office):** on Enterprise the prompt is inlined into the `streamAssist` body, so an agent that re-sends file content every turn is exactly what trips rate limits. With an upload transport available the server now moves that content into context-file attachments instead:

- files the latest user message references by path (PDF, Word, PowerPoint, images, long source files) are read from disk and attached — the model reads them natively, including images;
- an oversized prompt, or large fenced code blocks inside it, is spilled into one `.txt` attachment and replaced by a short notice;
- uploads are cached by content hash (`discovery_engine_upload_cache_sec`), so the same file is uploaded once and then referenced by `fileId` on later turns.

```jsonc
{
  "enterprise_auto_attach_local_files": true,   // attach referenced local files
  "enterprise_auto_attach_min_bytes": 0,        // 0 = reuse bridge_large_code_upload_bytes
  "enterprise_attach_large_prompt": true        // spill oversized prompts into a .txt
}
```

Both switches are no-ops without an upload transport (no captured template, no `discovery_engine_upload_endpoint`, no widget `csesidx`): in that case content stays inline so nothing is silently dropped. Verify with the log line `Enterprise: calling the Discovery Engine widgetStreamAssist API (files=1, bearer=true, model="…")` — `files=0` means nothing was attached.

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
- **Single-turn only**: Each request is an independent conversation. Multi-turn context is simulated by including previous messages in the prompt. The Gemini Enterprise session is one per process and is not persisted, so continuing a chat the next day relies on the client resending its history (and any attachment from yesterday must be re-attached).
- **Media depends on the account**: `/v1/images/generations` and `/v1/videos/generations` can only return what the signed-in account's Gemini web is allowed to generate. A tenant without image/video access answers with text, and the endpoint reports exactly that instead of returning a placeholder.
- **Rate limits**: Google may throttle high-frequency requests. The server retries automatically but sustained heavy use may be blocked.

## License

MIT License.
