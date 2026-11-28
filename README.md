<p align="center">
  <img src="./docs/banner.svg" alt="gemini-web2api banner" width="100%" />
</p>

<p align="center">
  <a href="https://github.com/Hau04/gemini-web2api/actions"><img src="https://img.shields.io/github/actions/workflow/status/Hau04/gemini-web2api/build-windows.yml?style=flat-square&label=CI" alt="CI Status" /></a>
  <a href="https://github.com/Hau04/gemini-web2api/blob/main/go.mod"><img src="https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go Version" /></a>
  <a href="https://github.com/Hau04/gemini-web2api/blob/main/Dockerfile"><img src="https://img.shields.io/badge/Docker-Scratch_Image-2496ED?style=flat-square&logo=docker&logoColor=white" alt="Docker" /></a>
  <a href="./LICENSE"><img src="https://img.shields.io/badge/License-MIT-22c55e?style=flat-square" alt="License" /></a>
</p>

<p align="center">
  <strong>English</strong> · <a href="./README.zh-CN.md">简体中文</a>
</p>

> **Disclaimer:** This project is intended for personal and educational use only. It is an unofficial proxy for Gemini web sessions and is not affiliated with Google. Automating web sessions may violate Google's Terms of Service and could result in account restrictions. Use at your own risk.

---

## Overview

**`gemini-web2api`** is a self-hosted Go HTTP gateway that turns your existing **Gemini Consumer** (`gemini.google.com`), **Gemini Enterprise** (`vertexaisearch.cloud.google.com`), and **Gemini Business** (`business.gemini.google`) web sessions into standard **OpenAI-compatible** and **Google Gemini-compatible** APIs.

Built to run cleanly inside **Docker** (or as a standalone Go binary), it pairs a browserless **Direct Cookie Mode** with an optional **Chrome MV3 Live Web Bridge** extension — and supports **isolated multi-account management** through a built-in Web GUI (`/ui`).

<p align="center">
  <img src="./docs/architecture.svg" alt="gemini-web2api architecture" width="100%" />
</p>

---

## Highlights

| Capability | What It Does |
| :--- | :--- |
| **Multi-Protocol API Surface** | OpenAI Chat Completions (`/v1/chat/completions`), OpenAI Responses (`/v1/responses`), Image & Video Generation (`/v1/images/generations`, `/v1/videos/generations`), Google Gemini (`/v1beta/models/{model}:generateContent` & `:streamGenerateContent`), plus model diagnostics (`/v1/model-info`, `/v1/model-probe`). |
| **Multi-Account Isolation** | Run multiple Google/Gemini accounts in one server instance with completely isolated `cookie.json`, Discovery Engine templates, learned model caches, bridge hubs, and conversation threads. Differentiate accounts directly in the API URL (`/a/<account_id>/v1`). |
| **Embedded Account GUI (`/ui`)** | Manage accounts right in your browser: add/delete accounts, paste or upload `cookie.json`, inspect live cookie & bridge health (`Cookies Ready`, `Session Expired`, `No Cookies`, `SAPISID`, `1PSID`, `XSRF`), and copy per-account API URLs in one click. |
| **Dual Transport Engine** | **Direct Cookie Mode** serves requests headlessly even when your browser is closed; **Live Web Bridge** (Chrome MV3) automatically handles cookie/XSRF syncing, unmapped Enterprise picker models, and oversized prompt spillover. |
| **Agent & Tool-Calling Ready** | Full OpenAI/Google tool calling, parallel tool calls, MCP/connector passthrough, `prevent_deep_research` execution guard for premium Gemini 4 models, optional `tool_model` routing, and instant local handling of client title-summary prompts. |
| **Enterprise Attachments & Pacing** | Automatic Discovery Engine `widgetStreamAssist` & `widgetAddContextFile` replay, local file auto-attachment, anti-ban pacing limits, 429 cooldown persistence, and expiry webhook alerts (`ntfy`, `discord`, `slack`, `generic`). |

---

## Quick Start (Docker — Recommended)

```bash
git clone https://github.com/Hau04/gemini-web2api.git
cd gemini-web2api
cp config.example.json config.json
mkdir -p data accounts
docker compose up -d --build
```

Once running:

- **Default OpenAI Base URL:** `http://localhost:8081/v1`
- **Default Google Base URL:** `http://localhost:8081/v1beta`
- **Account Manager GUI:** `http://localhost:8081/ui`

### Persistent Volumes (`docker-compose.yml`)

- `./config.json:/app/config.json` — server settings & API keys
- `./data:/app/data` — persistent default session cookies & templates
- `./accounts:/app/accounts` — persistent isolated per-account directories

> **Tip (Docker Networking):** If Gemini returns empty responses under Docker's default bridge NAT, switch to `network_mode: host` in `docker-compose.yml`.

---

## Running from Source

Requires **Go 1.26+**.

```bash
git clone https://github.com/Hau04/gemini-web2api.git
cd gemini-web2api
cp config.example.json config.json
go run .
```

---

## Multi-Account Isolation & Web GUI

Open **`http://<host>:8081/ui`** to manage multiple Gemini accounts inside a single container.

| Account | OpenAI Base URL | Google Base URL | Extension Server URL |
| :--- | :--- | :--- | :--- |
| `default` | `http://<host>:8081/v1` | `http://<host>:8081/v1beta` | `http://<host>:8081` |
| `work` | `http://<host>:8081/a/work/v1` | `http://<host>:8081/a/work/v1beta` | `http://<host>:8081/a/work` |
| `personal` | `http://<host>:8081/a/personal/v1` | `http://<host>:8081/a/personal/v1beta` | `http://<host>:8081/a/personal` |

You can also select an account on unprefixed routes using the `X-Gemini-Account: <id>` header or `?account=<id>` query parameter. Every API response echoes `X-Gemini-Account`.

### Account Management REST API

| Endpoint | Description |
| :--- | :--- |
| `GET /api/accounts` | List all accounts and live cookie/bridge health |
| `POST /api/accounts` | Create an isolated account (`id`, `label`, optional `raw`/`cookie`) |
| `DELETE /api/accounts/{id}` | Delete a named account and its isolated directory |
| `PUT /api/accounts/{id}/cookies` | Upload or paste `cookie.json` or raw `Cookie:` header string |
| `DELETE /api/accounts/{id}/cookies` | Clear stored cookies for an account |

> When `bridge_token` or `api_keys` is configured, `/api/accounts/*` requires `X-Bridge-Token` or `Authorization: Bearer <key>`. The `/ui` page prompts you once to unlock.

---

## Browser Extension Setup

The bundled Chrome MV3 extension (`extension/`) keeps session cookies, XSRF tokens, and Discovery Engine templates synced automatically — and provides the Live Web Bridge fallback.

1. Open `chrome://extensions` and enable **Developer mode**.
2. Click **Load unpacked** and select the `extension/` directory.
3. Sign in to `gemini.google.com` or `vertexaisearch.cloud.google.com`.
4. Click the extension icon, enter your **Server URL** (e.g. `http://localhost:8081` or your Cloudflare Tunnel URL), optional **Account ID** (e.g. `work` for a second Chrome profile), and optional **Bridge Token**, then click **Save & Sync Now**.

---

## Configuration (`config.json`)

See [`config.example.json`](./config.example.json) for all options. Key fields:

| Field | Default | Description |
| :--- | :--- | :--- |
| `port` / `host` | `8081` / `"0.0.0.0"` | Server bind address and port |
| `api_keys` | `[]` | Allowed API keys (`Authorization: Bearer`, `x-api-key`, `x-goog-api-key`, `?key=`) |
| `bridge_token` | `""` | Shared secret protecting `/bridge/*` and `/api/accounts/*` |
| `cookie_file` | `"cookie.json"` | Cookie file path for the default account |
| `accounts_dir` | `""` | Directory for isolated accounts (defaults to `<cookie-dir>/accounts`) |
| `default_model` | `"gemini-3.6-flash"` | Fallback model when `model` is omitted |
| `tool_model` | `""` | Optional model override for tool-enabled requests |
| `prevent_deep_research` | `true` | Keeps tool-calling turns on client tools instead of Deep Research |
| `base_url` | `"https://gemini.google.com"` | Default upstream Gemini origin |
| `proxy` | `""` | Optional HTTP/HTTPS/SOCKS5 proxy URL |
| `impersonate` | `""` | Optional TLS fingerprint profile |
| `notify_webhook` | `""` | Session-expiry alert webhook (`ntfy`, `discord`, `slack`, `generic`) |

---

## API Examples

### OpenAI Chat Completions (Isolated Account)

```bash
curl http://localhost:8081/a/work/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-gemini" \
  -d '{
    "model": "gemini-3.6-flash",
    "messages": [{"role": "user", "content": "Hello!"}],
    "stream": true
  }'
```

### Google Gemini API

```bash
curl "http://localhost:8081/v1beta/models/gemini-3.6-flash:generateContent?key=sk-gemini" \
  -H "Content-Type: application/json" \
  -d '{"contents":[{"parts":[{"text":"Hello!"}]}]}'
```

---

## License

MIT — see [LICENSE](./LICENSE).
