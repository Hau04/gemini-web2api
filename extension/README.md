# Gemini Web2API - Live Web Bridge & Cookie Plugin

This browser extension allows `gemini-web2api` to interact directly with your open browser tab on **Gemini Enterprise** (`vertexaisearch.cloud.google.com`) or **Gemini Web** (`gemini.google.com`).

---

## 🛡️ Anti-Ban Live Web Bridge Mode (Recommended)

In this mode, you **do not use or export cookies**. Instead:
1. You keep your Gemini Enterprise / Web tab open in Chrome or Edge.
2. The extension's content script runs inside the open web page.
3. When `gemini-web2api` receives an API request (e.g. from WorkBuddy or an OpenAI client), it sends the prompt to the browser tab.
4. The extension enters the prompt into the real web page, clicks send, observes Gemini's response as it streams, and returns the tokens in real time back to `gemini-web2api`.

**Why this prevents bans**:
- Google's servers only see traffic coming from your real, authentic browser tab with genuine mouse movements, TLS signatures, and DOM rendering.
- No headless HTTP requests or cookie extraction scripts to trigger Cloudflare or Google bot detection.

---

## How to Install (Chrome, Edge, Brave, Opera)

1. Open your browser's extensions page:
   - **Chrome**: `chrome://extensions/`
   - **Edge**: `edge://extensions/`
   - **Brave**: `brave://extensions/`
2. Enable **"Developer mode"** (toggle in the top-right corner).
3. Click **"Load unpacked"**.
4. Select the `extension/` folder from this repository.

---

## How to Run with the Live Web Bridge

1. Start `gemini-web2api`:
   ```bash
   ./gemini-web2api --port 8081
   ```
2. Open your browser tab:
   - For Enterprise: `https://vertexaisearch.cloud.google.com/`
   - For Consumer: `https://gemini.google.com/`
3. Look at the bottom-right corner of the page:
   - You will see a small badge: `🟢 Web2API Bridge: Connected (:8081)`
4. Send requests to `http://localhost:8081/v1/chat/completions` (from WorkBuddy, Cursor, curl, etc.).
   - The badge will pulse: `⚡ Web2API: Generating reply...`
   - The web page will type the prompt and stream the reply directly back to your API client!

---

## Fallback Mode: Cookie Export

If you ever want headless background operation without an open browser window, you can still click the extension icon on your toolbar and click **"Download cookie.json"**.

---

## 🔄 Auto Cookie Sync (v1.4.0+)

The extension's background service worker automatically pushes your Google session cookies to the local `gemini-web2api` server (every 2 minutes, on tab updates, and when cookies change). This enables **Direct Cookie Mode fallback**:

- **Chrome Memory Saver**: When Chrome freezes your background Gemini tab, the server detects the freeze and instantly routes new requests via direct RPC using your synced cookies — no 240s timeout.
- **Bridge stall recovery**: If the bridge fails mid-turn with zero content (tab froze during generation), the server transparently retries via direct mode.
- **Session expiry alerts**: If Google rejects your cookies (HTTP 401/redirect to sign-in), the popup shows **"Session expired — open gemini.google.com to refresh"**.

Check the **"Cookie Sync (Direct Mode)"** indicator in the extension popup to verify sync status. No configuration is needed — it works automatically when the server is running on `localhost:8081`.

### How It Works

1. The background service worker reads your Google session cookies (`__Secure-1PSID`, `SAPISID`, etc.) and extracts the XSRF token from any open Gemini tab.
2. It POSTs them to `localhost:8081/bridge/cookies`, which atomically writes them to the server's cookie file.
3. The server's direct-mode client (CookieCache) hot-reloads the file on the next request.
4. When the server detects a frozen tab or bridge timeout with zero content, it marks the bridge as **degraded** and routes subsequent requests via direct cookie mode for 10 minutes (or until the tab recovers).
5. If the direct mode request fails with an auth error, the server records it and the popup shows a clear **session expired** message.
