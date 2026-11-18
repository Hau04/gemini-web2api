// Gemini Web2API Background Service Worker
// - Periodically pushes Google session cookies to the gemini-web2api server
//   (POST /bridge/cookies) so the server can answer via direct RPC mode whenever
//   the live tab bridge is unusable (frozen by Chrome Memory Saver) or CLOSED.
// - Watches Gemini tabs for discard/freeze (chrome.tabs.onUpdated -> discarded)
//   and reports state transitions to POST /bridge/frozen so the server instantly
//   re-routes in-flight and future requests to cookie mode.
//
// Server address & auth token are configurable from the extension popup
// (chrome.storage.local: serverUrl, bridgeToken) so the server can run on a
// remote NAS / VPS behind HTTPS (e.g. Cloudflare Tunnel) instead of localhost.

const DEFAULT_SERVER_URL = "http://localhost:8081";
const SYNC_ALARM = "web2api_cookie_sync";
const SYNC_PERIOD_MIN = 2;

const COOKIE_DOMAINS = [
  "google.com",
  ".google.com",
  "gemini.google.com",
  "business.gemini.google",
  "cloud.google.com",
  ".cloud.google.com",
  "vertexaisearch.cloud.google.com"
];

const COOKIE_KEY_ORDER = [
  "SID", "HSID", "SSID", "APISID", "SAPISID",
  "__Secure-1PSID", "__Secure-1PSIDTS", "__Secure-1PAPISID",
  "__Secure-3PSID", "__Secure-3PAPISID"
];

const GEMINI_URL_RE = /^https:\/\/(gemini\.google\.com|business\.gemini\.google|vertexaisearch\.cloud\.google\.com)\//;

// normalizeServerUrl strips trailing slashes and trailing /v1 so users can paste
// either the API base URL or the server root.
function normalizeServerUrl(raw) {
  let u = String(raw || "").trim();
  if (!u) return DEFAULT_SERVER_URL;
  if (!/^https?:\/\//i.test(u)) u = "http://" + u;
  u = u.replace(/\/+$/, "");
  u = u.replace(/\/v1$/i, "");
  return u;
}

function buildBridgeHeaders(token, extra) {
  const h = Object.assign({}, extra || {});
  if (token) h["X-Bridge-Token"] = token;
  return h;
}

// Export pure helpers for Node.js unit tests
if (typeof module !== "undefined" && module.exports) {
  module.exports = { normalizeServerUrl, buildBridgeHeaders };
}

async function getBridgeConfig() {
  try {
    const res = await chrome.storage.local.get(["serverUrl", "bridgeToken"]);
    return {
      url: normalizeServerUrl(res.serverUrl),
      token: String(res.bridgeToken || "").trim()
    };
  } catch (e) {
    return { url: DEFAULT_SERVER_URL, token: "" };
  }
}

async function collectCookies() {
  const cookieMap = new Map();
  for (const domain of COOKIE_DOMAINS) {
    try {
      const cookies = await chrome.cookies.getAll({ domain });
      for (const c of cookies) {
        cookieMap.set(c.name, c.value);
      }
    } catch (e) {}
  }

  const parts = [];
  for (const k of COOKIE_KEY_ORDER) {
    if (cookieMap.has(k)) {
      parts.push(`${k}=${cookieMap.get(k)}`);
    }
  }
  return {
    cookieString: parts.join("; "),
    sapisid: cookieMap.get("SAPISID") || "",
    hasPSID: cookieMap.has("__Secure-1PSID") || cookieMap.has("__Secure-3PSID")
  };
}

async function extractXsrfFromTab(tabId) {
  try {
    const results = await chrome.scripting.executeScript({
      target: { tabId },
      func: () => {
        const scripts = document.querySelectorAll("script");
        for (const s of scripts) {
          const m = s.textContent.match(/"SNlM0e":"([^"]+)"/);
          if (m) return m[1];
        }
        if (window.WIZ_global_data && window.WIZ_global_data.SNlM0e) {
          return window.WIZ_global_data.SNlM0e;
        }
        return "";
      }
    });
    if (results && results[0] && results[0].result) {
      return String(results[0].result);
    }
  } catch (e) {}
  return "";
}

async function syncCookiesNow(reason) {
  try {
    const { cookieString, sapisid, hasPSID } = await collectCookies();
    if (!sapisid && !hasPSID) {
      // Not signed in anywhere relevant — nothing useful to push.
      return;
    }

    // Try to grab the XSRF token from any open Gemini-ish tab (best effort).
    let xsrf = "";
    let authUser = "";
    try {
      const tabs = await chrome.tabs.query({ url: "*://gemini.google.com/*" });
      const entTabs = await chrome.tabs.query({ url: "*://vertexaisearch.cloud.google.com/*" });
      const bizTabs = await chrome.tabs.query({ url: "*://business.gemini.google/*" });
      const tab = [...tabs, ...entTabs, ...bizTabs].find((t) => t && t.id);
      if (tab) {
        xsrf = await extractXsrfFromTab(tab.id);
        const m = (tab.url || "").match(/\/u\/(\d+)\//);
        if (m) authUser = m[1];
      }
    } catch (e) {}

    const payload = { cookie: cookieString, sapisid };
    if (xsrf) payload.xsrf_token = xsrf;
    if (authUser) payload.auth_user = authUser;

    const cfg = await getBridgeConfig();
    const res = await fetch(`${cfg.url}/bridge/cookies`, {
      method: "POST",
      headers: buildBridgeHeaders(cfg.token, { "Content-Type": "application/json" }),
      body: JSON.stringify(payload)
    });
    if (res.ok) {
      const data = await res.json().catch(() => ({}));
      if (data && data.auth_error) {
        console.warn("[Web2API] Cookie auth error reported by server:", data.auth_error);
      }
      console.log(`[Web2API] Cookies synced to ${cfg.url} (${reason}): ${cookieString.length} bytes, xsrf=${!!xsrf}`);
    } else if (res.status === 401 || res.status === 403) {
      console.warn("[Web2API] Cookie sync rejected (HTTP " + res.status + ") — check bridge token in extension settings.");
    }
  } catch (e) {
    // Server offline or unreachable — stay silent; next alarm retries.
  }
}

async function reportFrozen(tab, frozen) {
  try {
    const cfg = await getBridgeConfig();
    await fetch(`${cfg.url}/bridge/frozen`, {
      method: "POST",
      headers: buildBridgeHeaders(cfg.token, { "Content-Type": "application/json" }),
      body: JSON.stringify({
        frozen: !!frozen,
        client_id: `bg_${tab.id}`,
        tab: new URL(tab.url).hostname
      })
    });
    console.log(`[Web2API] Reported tab frozen=${!!frozen} for ${tab.url}`);
  } catch (e) {}
}

// Track per-tab frozen state so we only report transitions (Chrome fires
// onUpdated multiple times per tab lifecycle event).
const frozenTabs = new Map();

// Runtime listeners — only register in a real extension environment
// (skipped under Node.js unit tests where `chrome` is undefined).
if (typeof chrome !== "undefined" && chrome.tabs && chrome.runtime) {
chrome.tabs.onUpdated.addListener((tabId, changeInfo, tab) => {
  if (!tab || !tab.url || !GEMINI_URL_RE.test(tab.url)) return;

  // Chrome exposes discarded (Memory Saver) as tab.discarded=true. When a tab is
  // discarded its content script is killed — from the server's point of view the
  // bridge is frozen/unreachable.
  const frozen = Boolean(tab.discarded);
  const prev = frozenTabs.get(tabId);
  if (prev === undefined || prev !== frozen) {
    frozenTabs.set(tabId, frozen);
    reportFrozen(tab, frozen);
  }

  // When a tab reloads/navigates, re-sync cookies (session may have refreshed).
  if (changeInfo.status === "complete") {
    syncCookiesNow("tab-update");
  }
});

chrome.tabs.onRemoved.addListener((tabId) => {
  frozenTabs.delete(tabId);
});

chrome.runtime.onInstalled.addListener(() => {
  chrome.alarms.create(SYNC_ALARM, { periodInMinutes: SYNC_PERIOD_MIN });
  syncCookiesNow("install");
});

chrome.runtime.onStartup.addListener(() => {
  syncCookiesNow("startup");
});

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm && alarm.name === SYNC_ALARM) {
    syncCookiesNow("alarm");
  }
});

// Also sync when a Google session cookie changes (login, token refresh).
// Chrome fires one event PER cookie — a login/refresh burst sets 5-10 cookies in
// under a second, so debounce: wait until the burst is quiet for 3s before syncing.
let cookieChangeTimer = null;
chrome.cookies.onChanged.addListener((changeInfo) => {
  const c = changeInfo && changeInfo.cookie;
  if (!c) return;
  if (COOKIE_KEY_ORDER.includes(c.name)) {
    if (cookieChangeTimer) clearTimeout(cookieChangeTimer);
    cookieChangeTimer = setTimeout(() => {
      cookieChangeTimer = null;
      syncCookiesNow("cookie-change");
    }, 3000);
  }
});

// Push the stored chat-mode override to the server (best effort).
async function pushChatModeNow() {
  try {
    const cfg = await getBridgeConfig();
    if (!cfg.url) return;
    const res = await chrome.storage.local.get(["chatMode"]);
    const mode = res.chatMode || "auto";
    await fetch(`${cfg.url}/bridge/chatmode`, {
      method: "POST",
      headers: buildBridgeHeaders(cfg.token, { "Content-Type": "application/json" }),
      body: JSON.stringify({ mode })
    });
  } catch (e) {
    // Server unreachable — the popup will show the stored value and retry on next change/sync.
  }
}

// Re-sync immediately when the user updates the server address/token in the popup.
chrome.runtime.onMessage.addListener((msg) => {
  if (msg && msg.action === "syncCookiesNow") {
    syncCookiesNow("manual");
    pushChatModeNow();
  }
});
}
