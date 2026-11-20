<!-- markdown-translator:38f14a416678b63795c58ff9041fc8761d1e138864beaf7a59854853f86ec386 -->
# Gemini-web2api

<p align="center">
  <img src="https://raw.githubusercontent.com/Sophomoresty/gemini-web2api/main/logo.png" width="200" alt="gemini-web2api logo">
</p>

[英语](README.md)|[中文文档](README.zh-CN.md)

高性能 Go 代理将 Google Gemini 的 Web 界面转换为 OpenAI 兼容的 API。零成本、跨平台、单一静态二进制文件。

这是对原始版本的完全 Go 重写[Python gemini-web2api](https://github.com/Sophomoresty/gemini-web2api)。它具有巨大的并发吞吐量、最小的内存占用以及增强的功能，例如**TLS 模拟**用于WAF绕过。

## 主要特点

* **可选 API 密钥**: 没有授权时`api_keys`配置后为空，OpenAI 风格的承载身份验证。
* **兼容 OpenAI**：直接替代`/v1/chat/completions`,`/v1/models`， 和`/v1/responses`（法典 CLI）。
* **多式联运（视觉）**：完全支持发送图像（`image_url`/base64) 到 Gemini，使用 Scotty 可恢复上传协议本机内置图像压缩。
* **TLS 模拟**：内置支持通过模仿 Chrome/Edge TLS 指纹来绕过 Cloudflare/WAF`tls-client`🔥.
* **工具调用**：全函数调用支持（OpenAI格式）。
* **多种型号**：Flash (3.6)、Extend Thinking（20k+ 字符输出）、Pro、Auto、Lite。
* **思考深度**：可通过调整推理`@think=N`后缀（0=最深，4=最浅）。
* **网页搜索**：内置互联网访问（Gemini 的本机搜索）。
* **高性能**：纯 Go 静态二进制文件，具有高并发 SSE 流吞吐量和最小的内存占用。
* **跨平台**：零运行时依赖性，单个静态二进制文件。
* **法典 CLI**：响应API（`/v1/responses`）用于 OpenAI Codex 集成。
* **双子座命令行界面**：谷歌原生API（`/v1beta/models`）以实现 Gemini CLI 兼容性。

## 快速入门

### 1.下载预构建的二进制文件（推荐）

从以下地址下载适用于您的操作系统（Linux、macOS、Windows）的预编译二进制文件[发布](https://github.com/Hau04/gemini-web2api/releases)页：

**Linux / macOS：**

```bash
chmod +x gemini-web2api
./gemini-web2api --port 8081
```

**Windows（PowerShell /命令提示符）：**

```powershell
.\gemini-web2api.exe --port 8081
```

### 2.通过Go工具链安装

如果您的系统上安装了 Go 1.22+：

```bash
go install github.com/Hau04/gemini-web2api@latest
```

这将编译二进制文件并将其直接放入您的`$GOPATH/bin`（或者`%USERPROFILE%\go\bin`）。从任何地方运行它：

```bash
gemini-web2api --port 8081
```

### 3. 从源代码构建

先决条件：Go 1.22+

```bash
git clone https://github.com/Hau04/gemini-web2api.git
cd gemini-web2api
```

构建后，运行可执行文件：

**Linux / macOS：**

```bash
go build -o gemini-web2api .
./gemini-web2api --port 8081
```

**Windows（PowerShell /命令提示符）：**

```powershell
go build -o gemini-web2api.exe .
.\gemini-web2api.exe --port 8081
```

服务器开始于`http://localhost:8081/v1`.

### 4.通过Docker运行

在通过 Docker 运行之前，复制示例配置文件：

```bash
cp config.example.json config.json
```

#### 选项 A：运行预构建的 GHCR 映像（无需 git 克隆）

```bash
docker run -d --name gemini-web2api -p 8081:8081 -v ./config.json:/app/config.json ghcr.io/hau04/gemini-web2api:latest
```

*（如果您没有`config.json`，简单地省略`-v ./config.json:/app/config.json`以默认设置运行）。*

#### 选项 B：本地构建并运行

```bash
docker build -t gemini-web2api .
docker run -d --name gemini-web2api -p 8081:8081 -v ./config.json:/app/config.json gemini-web2api
```

#### 选项 C：Docker Compose

```bash
docker compose up -d
```

> **笔记**: 撰写文件挂载`./data:/app/data`用于持久 cookie 存储。放`"cookie_file": "/app/data/cookie.json"`在你的`config.json`因此扩展同步的 cookie 在容器重新启动后仍然存在。 （安装`./cookie.json`直接也可以，但是 Docker 创建了一个*目录*如果该文件尚不存在 - data-dir 挂载会避免使用该脚枪。）
>
> **笔记**：如果您得到空回复（`content: null`）使用 Docker 的默认桥接网络，切换到主机网络：`docker run --network host ...`或添加`network_mode: host`在您的撰写文件中。这是由 Gemini 的上游拒绝来自某些 Docker NAT IP 范围的请求造成的。

### 5. 远程 NAS / Cloudflare 隧道设置（Cookie 同步模式）

在您的 NAS 上运行服务器，通过 Cloudflare Tunnel (HTTPS) 公开，并让浏览器扩展自动将 cookie 同步到它 — 然后您可以完全关闭浏览器：

**服务器`config.json`在网络存储上：**

```json
{
  "api_keys": ["sk-your-secret-api-key"],
  "cookie_file": "/app/data/cookie.json",
  "bridge_token": "pick-a-long-random-token",
  "notify_webhook": "https://ntfy.sh/your-unique-topic",
  "notify_format": "ntfy"
}
```

* **`bridge_token`**：保护所有人的共享秘密`/bridge/*`端点（cookie 推送、状态、冻结报告）。**必需的**当可以从互联网访问该端口时 - 没有它任何人都可以覆盖您的会话 cookie。
* **`api_keys`**: 保护`/v1/*`API 端点照常。
* **`notify_webhook`**：当会话 cookie 过期（Google 拒绝它们）时，服务器每次过期都会在此处发布警报。使用免费的[ntfy.sh](https://ntfy.sh)主题（在手机上订阅）、Discord webhook (`"notify_format": "discord"`), 松弛 (`"slack"`)，或任何通用 JSON webhook (`"generic"`).

**扩展弹出窗口 → 服务器连接：**

1. **服务器地址**：您的隧道 URL，例如`https://gemini.example.com`（尾随`/v1`自动处理）
2. **桥接令牌**： 相同`bridge_token`值作为服务器配置
3. 点击**立即保存并同步**— 扩展程序立即推送 cookie 并验证连接。

**每日流量：**

1. 打开`gemini.google.com`一次 → cookies 在几秒钟内自动推送（或单击“立即保存并同步”）
2. 关闭浏览器 — NAS 通过直接 cookie 模式继续提供服务
3. 几天后，当 XSRF 令牌过期时，您会收到推送通知→打开 Gemini 一次→自动重新同步

> **安全**：Cloudflare Tunnel 已为您提供 HTTPS。这`bridge_token`添加应用程序级身份验证，因此仅隧道 URL 不足以推送 cookie。保留两者设置。

## 客户端配置

### Cherry Studio / ChatBox / 任何 OpenAI 客户端

|领域 |价值|
| -------- | -------------------------------------------------------------------------------- |
|基本网址 |`http://localhost:8081/v1`|
| API 密钥 |任何`api_keys`价值来自`config.json`;如果没有配置的话，什么都可以|
|型号|`gemini-3.5-flash-thinking`|

### 卷曲

#### bash/macOS/Linux

```bash
curl http://localhost:8081/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-key" \
  -d '{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"Hello!"}]}'
```

#### PowerShell（Windows）

```powershell
curl.exe --% http://127.0.0.1:8081/v1/chat/completions -H "Content-Type: application/json" -H "Authorization: Bearer sk-your-key" -d "{\"model\":\"gemini-3.5-flash\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello!\"}]}"
```

> 注意：在 Windows PowerShell 上，使用`curl.exe`和`--%`因此 PowerShell 不会重新解释 JSON 引用或curl 选项。

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

### 双子座命令行界面

```bash
export GEMINI_API_KEY=none
export GOOGLE_GEMINI_BASE_URL=http://localhost:8081
gemini
```

支持 Google 本机 API 端点：

* `GET /v1beta/models`— 列出型号
* `POST /v1beta/models/{model}:generateContent`— 非流媒体
* `POST /v1beta/models/{model}:streamGenerateContent`— 流媒体（上交所）

## WorkBuddy / MCP 兼容性

面向会驱动工具、连接器、技能与 MCP 服务器的代理运行时（WorkBuddy、Claude 风格 agent、OpenAI SDK）：

| 能力 | 行为 |
| --- | --- |
| `GET /v1/models` | 每个别名都带 `capabilities`（`tools`、`connectors`、`mcp`、`vision`、`attachments`、`json_mode`、`streaming`）、`context_window`、`premium`、`research_prone`，客户端可在启用功能前先做能力判断 |
| `GET /v1/models/{id}` | 单模型查询（200/404），代理运行时用来校验配置的模型 id |
| `parallel_tool_calls: false` | 在提示词中强制：每轮只能输出**一个** `tool_call` 块 |
| 重复/无名工具 | MCP 与连接器合并后按名称去重；`{"type":"web_search"}` 这类没有函数名的内置声明会被跳过，不会污染提示词 |
| MCP 工具名 | `mcp__server__tool`、`server.tool` 等带命名空间的名称原样透传 —— 它们由你的客户端执行，不会被 Gemini 接管 |
| 技能 | 支持纯 `system` 长指令（技能本质就是指令包）；`/v1/responses` 与 `/v1/chat/completions` 同样完整支持 |

### 高级模型不会掉进 Deep Research

Neon、Hydrogen、Carbon、Argon 被标记为 `research_prone`：Gemini 界面很容易把它们切到 Deep Research，而那个模式只读公开网页、**无法调用你的工具、连接器或 MCP**。开启 `prevent_deep_research`（默认 `true`）后，带工具的请求：

- 丢弃上游研究/浏览类工具（`google_search`、`web_search`、`web_search_preview`、`deep_research`、`url_context`、`browse` 等），保留你真正的工具；
- 在 `# Tool Use` 段里注入 `EXECUTION MODE`：Deep Research 与内置搜索被禁用，关于本机、仓库、文件、应用的事实必须来自 `tool_call`，缺数据就先发工具调用而不是去研究；
- 响应头返回 `X-Deep-Research: disabled`，日志输出 `Execution-mode guard applied for premium model "gemini-4-neon" …`。

```jsonc
{ "prevent_deep_research": true, "tool_model": "gemini-3.8-flash" }
```

若想让所有带工具的请求都走更稳定、更便宜的工具调用模型，`tool_model` 依然优先生效。

## 真实企业版日志中的两种失败如何排查

**`context-file upload rejected (HTTP 400): One or more required fields are missing.`** —— widget 的 `widgetAddContextFile` 需要 `configId`；既没有捕获的上传模板、也没有 `discovery_engine_config_id` 时，服务器只能推导出 URL，推不出字段。现在不再浪费这次请求，而是直接报出修复方式：

```
cannot attach "code_context.txt": the Discovery Engine widget configId is unknown and the widget rejects uploads without it —
send one message in the Gemini Enterprise tab with the extension enabled …, or set "discovery_engine_config_id"
```

启动横幅也会如实显示，而不是给出一个看起来可用的回退。真的收到 400 时，错误里会列出实际发送的字段（`sent fields: …`），缺哪个一目了然。

**`modelId=<none>` /「还是 Auto」** —— `discovery_engine_models` 为空，什么都没发送。两条路：

1. **自动：** 在开启扩展的情况下，到 Gemini Enterprise 标签页里随便发一条消息。它自己的 `widgetStreamAssist` 请求会被捕获，租户真实的 `assistGenerationConfig.modelId`（以及 `configId`、`session`）会被复用，日志变成
   `Enterprise: calling the Discovery Engine widgetStreamAssist API (…, requested="gemini-4-hydrogen", modelId="gemini-2.5-pro", source=captured)`，
   `/v1/model-info` 显示 `"source": "captured"`。
2. **显式：** 直接做别名映射，精确可控。

`source=captured` 的含义是「已经在发送租户认可的模型，但那是标签页当时选中的那个」；想要固定某个模型就配置映射。之前日志打印 `model="gemini-4-hydrogen"` 会让人以为别名被发送了 —— 实际上从来没有。

### 让下拉框真正生效的路由规则

企业版里只有两条路能拿到你指定的模型：

1. **直连模式** —— 在 widget 请求里带 `assistGenerationConfig.modelId`。前提是知道租户 id（`discovery_engine_models` 映射、直接使用租户 id、配置的默认值，或从标签页捕获请求里学到的 id）。
2. **桥模式** —— 扩展在标签页下拉框里点选。只要下拉框里有该模型就一定生效。

所以路由现在会**看模型**：直连模式报不出你请求的模型时，请求改走桥，而不是被租户的 auto 模型悄悄回答 —— 这正是「我选了 Hydrogen，还是 Auto」的根因：

```
Keeping the browser bridge: direct mode has no tenant model id for "gemini-4-hydrogen" …
```

响应头带 `X-Model-Route: bridge-picker`，`/v1/model-info` 显示 `"source": "bridge-picker"`、`selection_live: true`。

两点注意：

- **标签页必须保持打开且未休眠。** Chrome 的内存节省会把后台标签页冻结，冻结即视为断开，桥不可用 → 回退直连 → 租户 auto。日志会明确写出，`/bridge/status` 里 `frozen: true`。
- **已映射的别名完全不依赖标签页。** 一旦 `discovery_engine_models` 里有了 id，直连模式即可服务请求（附件 `files=N` 也依赖这条）。

## 「我选了 Hydrogen，为什么还是 Auto 回答」—— 模型选择诊断

实际回答的模型由三处决定，每一处都可能静默回退。`GET /v1/model-info?model=gemini-4-hydrogen` 会直接告诉你当前是哪种情况：

```jsonc
{
  "requested": "gemini-4-hydrogen",
  "origin": "discovery_engine",
  "upstream": { "model_id": "", "source": "tenant-default", "selection_live": false },
  "discovery_engine_models": {},
  "warnings": ["Gemini Enterprise model selection is INACTIVE: discovery_engine_models is empty …"]
}
```

`upstream.source` 的含义：

| `source` | 含义 |
| --- | --- |
| `mapping` | 命中 `discovery_engine_models`，租户会用映射后的模型回答 |
| `passthrough` | 客户端直接指定了租户模型 id（`gemini-2.5-pro`） |
| `configured-default` | 别名未映射，使用了 `discovery_engine_default_model_id` |
| `tenant-default` | **什么都没选 —— 由租户决定，通常就是「Auto」** |
| `alias` | 消费版：别名本身就决定路由 |
| `bridge-picker` | 企业版没有租户模型 id：请求走浏览器桥，由标签页的下拉框选择模型 |
| `captured` | 企业版没有映射：复用从标签页捕获请求中得到的租户 `modelId` |

每个聊天响应都会在响应头里给出同样的结论（模型自己说它是什么并不可靠，那只是生成文本）：

```
X-Resolved-Model: gemini-4-hydrogen
X-Upstream-Model: tenant-default
X-Upstream-Model-Source: tenant-default
X-Model-Selection: ignored-by-tenant      # live | ignored-by-tenant
```

**企业版修复方式：** 把别名映射到租户真正提供的模型 id（可在标签页自身 `widgetStreamAssist` 请求的 `assistGenerationConfig.modelId` 里看到）：

```jsonc
{ "discovery_engine_models": { "gemini-4-hydrogen": "<tenant model id>", "gemini-4-neon": "<tenant model id>" } }
```

如果租户的模型列表里根本没有 Hydrogen/Neon，任何映射都变不出来 —— 请把它映射到租户提供的相近模型（例如它的 4 Argon 条目）。

**Bridge 模式：** 扩展在标签页下拉框里选择后会把结果回传。模型不在下拉列表时，不再静默变成 Auto，而是：

```
MODEL SELECTION WARNING: the extension could not select "gemini-4-hydrogen" in the tab (picker currently shows "Auto") …
```

`/bridge/status` 也会多出 `model_selection: { requested, applied, ok }`，随时可查。

## 可用型号

|型号|描述 |输出|
| -------------------------------- | ------------------------------------------------------------------ | ---------------- |
|`gemini-4-argon`|复杂任务的前沿模型（需要 Pro）|**~20k+ 字符**|
|`argon`,`gemini-4`| Gemini-4-argon 的别名 |**~20k+ 字符**|
|`gemini-4-neon`|最新前沿模型（需企业版/Pro）|**~20k+ 字符**|
|`neon`、`gemini-4-neon-thinking`| gemini-4-neon 的别名 |**~20k+ 字符**|
|`gemini-4-hydrogen`|高速前沿执行模型|**~20k+ 字符**|
|`hydrogen`| Gemini-4-氢的别名 |**~20k+ 字符**|
|`gemini-3.8-flash`|最新全能机型（Gemini 3.8 Flash）|约 12k 个字符 |
|`gemini-3.7-flash`|全能机型（Gemini 3.7 Flash）|约 12k 个字符 |
|`gemini-3.6-flash`|全能机型（Gemini 3.6 Flash）|约 12k 个字符 |
|`gemini-3.5-flash`| gemini-3.6-flash 的别名 |约 12k 个字符 |
|`gemini-3.5-flash-thinking`|延伸思维，最长产出 |**约 20,000 个字符**|
|`gemini-3.5-flash-thinking-lite`|适应性思维深度|约 15,000 个字符 |
|`gemini-3.1-pro`|高级数学和代码（需要 Pro cookie）|约 12k 个字符 |
|`gemini-auto`|汽车选型|变化 |
|`gemini-flash-lite`|最快的答案，轻量级 | 〜10k 个字符 |

### 思考深度

附加`@think=N`任意模型名称来控制推理逻辑（对应Web UI Thinking级别子菜单）：

```text
gemini-4-argon@think=0              # High (deepest reasoning, default for thinking models)
gemini-4-argon@think=2              # Medium
gemini-4-argon@think=4              # Low (default for standard models)
```

### 聊天会话模式（新聊天与继续聊天）

在**桥接模式**，每个请求要么在打开的 Gemini 选项卡中启动新的聊天，要么附加到当前打开的聊天中。控制它的三种方法（按优先顺序排列）：

1. **每个请求的型号后缀**— 追加`@new`（或者`@fresh`/`@newchat`）强制进行新的聊天，`@continue`（或者`@cont`）以继续附加到打开的聊天中。结合`@think=N`以任意顺序：
   ```text
   gemini-4-hydrogen@new                # this request starts a brand-new chat
   gemini-4-hydrogen@think=0@continue   # deep thinking, keep the open chat (incl. Gemini memory)
   ```
2. **扩展弹出“聊天模式”选择器**—*自动（推荐）*/*总是有新的聊天*/*继续打开聊天*。选择被推送到服务器（`POST /bridge/chatmode`）并持续存在直到发生改变。和*继续打开聊天*，首先单击 Gemini 历史记录侧栏中的任何旧聊天 - API 轮流然后继续添加到该对话。
3. **自动启发式（默认）**— 全新的客户端对话（例如 WorkBuddy“新聊天”）启动新的浏览器聊天；后续回合继续进行。

### 模型检查头

每个 API 响应（JSON 和流式 SSE）都包含指示实际模型路由的 HTTP 响应标头：

* `X-Resolved-Model`：实际路由到的后端模型标识符（例如`gemini-4-argon`).
* `X-Model-Fallback: true`：每当请求未映射的模型并回退到默认模型时都包含在内。

## 高级功能（独家）

### 1.动态模型发现和上游路由头

当使用帐户 cookie 进行身份验证时，代理会动态查询 Gemini 的`otAQ7b`启动期间进行 RPC 以发现可用模型和帐户层容量。它附加所需的上游路由标头（`x-goog-ext-525001261-jspb`）匹配 Web 应用程序行为，允许 Gemini 4 Argon 等模型正确路由。

### 2. 视觉/多模式上传

与忽略图像的 Python 版本不同，此 Go 端口主动拦截`image_url`有效负载（base64 编码和外部 HTTP URL）。
它启动后台 Scotty 可恢复上传会话，将 base64 图像转换为 Google 内部 WIZ blob，并将 WIZ 图像引用直接传递给 Gemini。
只需发送 OpenAI 标准多模式有效负载，它就会起作用！

### 3. TLS 模拟（绕过 WAF）

如果您在 Google 使用 403 Forbidden 阻止 IP 的数据中心（如 AWS/DigitalOcean）中运行此工具，您可以模拟真实浏览器的 TLS 签名：

```bash
./gemini-web2api --impersonate chrome_120
```

或者在`config.json`:`"impersonate": "chrome_120"`。支持的配置文件包括`chrome_112`到`chrome_130`,`edge`， 和`safari`.

## 可选：Pro 和 Argon 的 Cookie

匿名访问适用于 Flash 模型，但是`gemini-4-argon`和`gemini-3.1-pro`需要经过身份验证**谷歌人工智能专业版/高级版**帐户会话。如果没有身份验证，上游会将请求路由到 Flash。

```bash
./gemini-web2api --cookie-file gemini-auth.json
```

### 如何获取cookies

1. 打开 Chrome，转到[gemini.google.com](https://gemini.google.com)并使用 a 登录**双子座高级版**谷歌帐户
2. 打开 DevTools (F12) → 应用程序 → Cookies →`https://gemini.google.com`
3. 复制这些 cookie 值：`SID`,`HSID`,`SSID`,`APISID`,`SAPISID`,`__Secure-1PSID`
4. 创造`cookie.txt`以这种格式：

```text
SID=your_sid; HSID=your_hsid; SSID=your_ssid; APISID=your_apisid; SAPISID=your_sapisid; __Secure-1PSID=your_1psid
```

或者如果您知道自己的具体情况，请使用 JSON 格式`SAPISID`散列需求：

```json
{ "cookie": "SID=xxx; HSID=xxx; ...", "sapisid": "your_sapisid_value" }
```

**替代方案（浏览器扩展 - 自动）**:

* **内置扩展**：使用中包含的浏览器扩展`extension/`目录！跟随[`extension/README.md`](extension/README.md)要在 Chrome/Edge 中加载解压后的文件，请打开 Gemini Web 或 Gemini Enterprise，然后单击“一键下载 cookie.json”。
* **自动 Cookie 同步 (v1.4.0+)**：该扩展现在包含一个后台服务工作者，可以自动将您的 Google 会话 cookie 推送到`localhost:8081`每 2 分钟，在选项卡更新和 cookie 更改时。这为**直接 Cookie 模式后备**— 当 Chrome 冻结您的 Gemini 选项卡（内存保护程序）或网桥停止运行时，服务器会使用您同步的 cookie 自动切换到直接 RPC 模式，无需手动干预。检查扩展弹出窗口的“Cookie 同步（直接模式）”指示器以验证其是否处于活动状态。
* **3rd 方扩展**：或者使用 Chrome Web Store / Firefox Add-ons 中的“Cookie-Editor”，导出为标题字符串，然后保存到`cookie.txt`.

### 经过身份验证的帐户路径和 XSRF 令牌

如果登录的 Gemini 页面 URL 包含帐户索引，例如：

```text
https://gemini.google.com/u/1/app/...
```

放`auth_user`到该索引。经过身份验证的 Web 请求可能还需要页面 XSRF 令牌。在渲染的 Gemini 页面源代码中，此令牌公开为`SNlM0e`;将其传递为`xsrf_token`在`config.json`。服务器将其作为`at`表单字段。

例子：

```json
{
  "cookie_file": "/app/cookie.txt",
  "auth_user": "1",
  "xsrf_token": "AOOh0P...",
  "gemini_bl": "boq_assistant-bard-web-server_YYYYMMDD.xx_p0"
}
```

如果经过身份验证的请求返回 HTTP 400`xsrf`错误，刷新Gemini Web，更新`xsrf_token`，并确保`auth_user`匹配`/u/<index>/`浏览器 URL 的一部分。

专业路由需要**双子座高级版**（付费订阅）。免费的 Google 帐户 cookie 将进行身份验证，但会默默地退回到 Flash。

### 🛡️ 反封禁 Live Web Bridge（推荐用于企业和 Web）

要完全消除 Google 帐户或 cookie 禁令的风险，您无需导出 cookie。你可以运行`gemini-web2api`在打开的浏览器选项卡旁边：

1. 从以下位置加载解压的扩展[`extension/`](extension/)进入 Chrome 或 Edge (`chrome://extensions`→ 开发者模式 → 加载解压）。
2. 保留你的**双子座企业**(`vertexaisearch.cloud.google.com`） 或者**双子座网**(`gemini.google.com`) 选项卡打开。
3. 开始`gemini-web2api`:
   ```bash
   ./gemini-web2api --port 8081
   ```
4. 浮动状态徽章将出现在您的 Gemini 网页上：`🟢 Web2API Bridge: Connected (:8081)`.
5. 任何请求发送至`http://localhost:8081/v1/chat/completions`（来自 WorkBuddy、Cursor 或 cursor）会自动通过您的实时浏览器选项卡进行路由，直接从 Google 的 Web UI 传输令牌。

#### 直接 PDF/长代码文件上传和反禁令保护（桥接模式）

使用 Live Web Bridge 运行时，`gemini-web2api`包括针对重复代理工具循环和 Google 频率禁令的内置保护：

* **直接文件、PDF 和长代码上传**：开放人工智能`file`/`input_file`/`document`/`image_url`零件会直接上传到您打开的 Gemini 选项卡中（无需 cookie）。对于直接`localhost`代理请求（例如 WorkBuddy）、引用的本地 PDF、Office 文档、图像和**长源代码文件（`>= bridge_large_code_upload_bytes`，默认 12 KB)**&#x4F1A;逐字自动附加（最多 25 MB，每转最多 5 个文件），并且仅在磁盘上编辑时自动刷新。
* **超大提示/工具结果溢出（`bridge_spillover_chars: 18000`)**：提示或巨大`read_file`超过 18 KB 的工具输出会自动上传为`.txt`附件而不是被猛击到网络输入框中，从而防止 UI 截断和重复重新读取。
* **Delta 转弯上下文 (`bridge_delta_context: true`)**：同一打开的 Gemini 选项卡中的后续轮次仅发送新附加的用户/工具消息，而不是每次都重新粘贴完整的对话历史记录和工具架构（刷新或新聊天时自动重置）。
* **自适应反禁令起搏和 429 断路器**：结合基础冷却时间（`bridge_min_interval_ms: 2500`），随机的类人抖动（`bridge_jitter_ms: 1500`），在繁重的编码循环期间渐进式突发扩展，滚动的 10 分钟窗口上限（`bridge_max_per_10min: 18`），以及自动指数冷却断路器（返回 HTTP`429`+`Retry-After`）如果 Google 发出速率限制信号。

所有选项都可以调整`config.json`:

```json
{
  "bridge_min_interval_ms": 2500,
  "bridge_jitter_ms": 1500,
  "bridge_max_per_10min": 18,
  "bridge_auto_upload_local_files": true,
  "bridge_large_code_upload_bytes": 12288,
  "bridge_spillover_chars": 18000,
  "bridge_delta_context": true
}
```

### Gemini Enterprise（Vertex AI 搜索）支持

如果您的组织使用**双子座企业**主办于`https://vertexaisearch.cloud.google.com/`，配置`base_url`:

```json
{
  "base_url": "https://vertexaisearch.cloud.google.com",
  "cookie_file": "/app/cookie.txt"
}
```

或者通过命令行传递：

```bash
./gemini-web2api --base-url https://vertexaisearch.cloud.google.com --cookie-file cookie.txt
```

什么时候`base_url`是定制的，`gemini-web2api`自动切换其`Origin`,`Referer`,`SAPISIDHASH`计算和 RPC 端点以匹配您的企业域。

**企业版模型选择：** Gemini Enterprise 默认使用助手自身配置的模型，除非请求里带 `assistGenerationConfig.modelId`。现在服务器会发送它，客户端指定 `gemini-4-hydrogen` 就不会再被静默降级为租户的「auto」：

```jsonc
{
  "discovery_engine_models": { "gemini-4-hydrogen": "gemini-2.5-flash", "argon": "gemini-2.5-pro" },
  "discovery_engine_default_model_id": ""
}
```

值必须是租户接受的模型 id（可在标签页自身 `widgetStreamAssist` 请求的 `assistGenerationConfig.modelId` 里看到）。本身就像上游 id 的名字（`gemini-2.5-pro`、`gemini-3-pro`）会原样传递；`argon`/`hydrogen`/`carbon` 这类本地别名不会透传（租户不认识）。重放捕获模板时同样会改写模板里写死的 modelId。

**企业版超长提示词不再走浏览器 `.txt` 溢出：** 该机制是为了绕过消费版输入框限制；Discovery Engine 的提示词直接内联在 `streamAssist` 的 JSON 里，因此超长请求现在直接走直连模式，不必等标签页（之前会出现 `context canceled` + 502）。只有直连完全不可用时才会退回桥接。

**Widget JWT 鉴权（解决企业版 502 的关键）：** `vertexaisearch.cloud.google.com` 上的 Discovery Engine widget 接口（`widgetStreamAssist` / `widgetCreateSession` / `widgetAddContextFile`）**不接受**消费版的 `SAPISIDHASH` 头，而是使用短期 HS256 JWT：

1. 带 Cookie 请求 `GET https://vertexaisearch.cloud.google.com/auth/getoxsrf?csesidx=<id>` → `{"xsrfToken":"<base64url HMAC 密钥>","keyId":"<kid>"}`
2. 用该密钥签发 `{"iss":<origin>,"aud":"https://content-us-discoveryengine.googleapis.com","sub":"csesidx/<id>","iat","exp":+300,"nbf"}`，header 带 `kid`
3. 先 `widgetCreateSession`，再 `widgetStreamAssist`，请求头为 `Authorization: Bearer <jwt>`、`Origin/Referer: <企业版 origin>`，body 带 `additionalParams: {"token":"-","origin":"ORIGIN_UNSPECIFIED"}`

只要服务器知道 **`csesidx`**（企业版标签页 URL 里的会话/配置序号），以上流程全自动：扩展在同步 Cookie 时会顺带采集 `?csesidx=…`，也可以手动配置 `discovery_engine_csesidx`。还需要 `discovery_engine_config_id`（应用发出的 widget `configId`，在标签页里上传一次文件即可被捕获）。`discovery_engine_location`（默认 `us`，或从 endpoint 推导）决定区域 widget 域名。

未采集到 `csesidx` 时会退回旧的 `?key=` + Cookie 方式 —— 这正是多数租户返回 `502` / `no answer text` 的路径。启动日志会打印当前使用的通道（`Widget auth: widget JWT (csesidx=…)` 或 `cookie fallback`）。

**编辑长代码文件，不再反复读取：** 当提示词引用了达到或超过 `enterprise_auto_attach_min_bytes`（默认复用 `bridge_large_code_upload_bytes`，8 KB）的源码文件时，文件会作为附件上传，提示词里只放一份简短清单：

```
# Attached workspace files (already loaded — do not re-read)
- /work/app/handlers.ts — 1,842 lines, 78 KB — attached as context file "handlers.ts"
These attachments are the current on-disk version as of this request. Do not call read_file ... re-attached
automatically on the next turn, so no verification read is needed.
```

这样往返成本的两半都被消掉：模型不必再花一轮去 `read_file`，也不必把整个文件内容当作工具参数回吐（这是最贵的输出）。WorkBuddy 写入补丁后，**下一次**请求会重新从磁盘读取并把新版本作为新附件上传，模型无需验证性重读即可看到自己的修改。上传按内容哈希缓存：每个文件*版本*上传一次，每轮零上传。

需要说清楚的边界：附件是只读上下文，真正的写入仍由 WorkBuddy 的写工具完成 —— 但那个方向是免费的，内容从模型流向你的磁盘，不会回流到 Gemini。

**图片是怎么被读到的：** `widgetAddContextFile` 就是 Gemini Enterprise 应用在输入框粘贴图片时自己发出的调用 —— 字节进入助手会话，`streamAssist` 通过 `fileId` 引用，多模态助手直接读图（视觉 token），而不是只拿到一个文件名。服务器按真实 MIME 原样上传图片（`image/png`、`image/jpeg` 等），绝不会把图片转成文本：`IsTextLike()` 对图片类型返回 false，所以图片不会被悄悄内联成乱码。日志可直接验证：`Enterprise context file "diagram.png" uploaded as image/png (240 KB, fileId=…)`。

图片来源有三种：OpenAI `image_url` 片段（data URL 或远程 URL，先抓取再上传）、Google `inlineData` 片段、以及提示词里引用的本地路径（`.png/.jpg/.jpeg/.webp/.gif/.bmp` 会被自动附加）。若租户拒绝上传，请求会给出可操作的报错，而不是丢弃图片。

**防封号的附件策略（大文件、图片、PDF/Office）：** 企业版把提示词内联在 `streamAssist` 请求体里，所以编码代理每轮重复发送文件内容正是触发限流/封号的原因。只要上传通道可用，服务器现在会把这些内容移出提示词、改成上下文文件附件：

- 最新一条用户消息里按路径引用的文件（PDF、Word、PowerPoint、图片、较长的源码）会从磁盘读取并作为附件上传，模型可直接读取，图片也能识别；
- 超长提示词或其中的大段代码会被拆进一个 `.txt` 附件，提示词只留简短说明；
- 上传结果按内容哈希缓存（`discovery_engine_upload_cache_sec`），同一文件只上传一次，之后复用 `fileId`。

```jsonc
{
  "enterprise_auto_attach_local_files": true,
  "enterprise_auto_attach_min_bytes": 0,
  "enterprise_attach_large_prompt": true
}
```

没有上传通道（没有捕获模板、没有 `discovery_engine_upload_endpoint`、没有 widget `csesidx`）时这两个开关自动失效，内容仍内联，不会被静默丢弃。可用日志 `Enterprise: calling the Discovery Engine widgetStreamAssist API (files=1, bearer=true, model="…")` 验证，`files=0` 表示没有附件。

**关闭浏览器后仍可使用附件（企业版）：** 消费版 Gemini Web 通过 `content-push.googleapis.com` 上传附件并在 Bard RPC 载荷中引用；Gemini Enterprise 没有这条链路，它把文件挂到**助手会话**上（`widgetAddContextFile` → `fileId`，随后 `streamAssist` 带上 `fileIds` + `session`）。因此纯 Cookie 模式以前会直接报 `this Gemini Enterprise origin needs the browser bridge for image attachments`。现在有三条路径：

1. **一次性捕获（推荐，零配置）**：加载扩展后，在企业版标签页里手动上传一个文件。net-hook 会记录这次上传请求，扩展把文件名 / MIME / Base64 内容替换为 `__W2A_FILE_NAME__` / `__W2A_FILE_MIME__` / `__W2A_FILE_B64__` 占位符后推送到 `POST /bridge/uploadtemplate`，并保存在 Cookie 文件旁的 `discovery_upload_template.json`。此后服务器用最新 Cookie 重放该请求来上传**所有**附件，浏览器可以一直关闭。用 `GET /bridge/uploadtemplate` 查看状态。
2. **直接配置端点**：没有捕获模板时，把 `discovery_engine_upload_endpoint` 设为标签页 DevTools 里的 `widgetAddContextFile` URL；若留空，服务器会把 `discovery_engine_endpoint` 中的 `widgetStreamAssist` 自动替换为 `widgetAddContextFile`。`discovery_engine_session`（如 `collections/default_collection/engines/my-engine/sessions/-`）和 `discovery_engine_config_id` 可固定会话与 Widget 配置，否则从捕获的请求体里读取。
3. **文本内联兜底**：完全没有上传通道时，纯文本类附件（代码、JSON、CSV、Markdown、日志，单个 ≤200 KB、总计 ≤400 KB）会带 `--- Attached file: … ---` 标记内联进提示词，编码代理仍能读到文件内容；图片 / PDF / Office 等二进制附件会返回明确错误，而不是被静默丢弃。

上传的文件与重放的 `streamAssist` 处于同一会话，后续对话会保留文件上下文。相同内容只会上传**一次**：同一请求内的重复文件共用一个 `fileId`，上传结果按内容哈希缓存 `discovery_engine_upload_cache_sec` 秒（默认 1800，设为 `0` 关闭），因此编码代理每轮重复发送同一批仓库文件时不再走上传；同时最多并行上传 4 个文件。

### WorkBuddy / AI 编码代理集成

`gemini-web2api`作为编码代理的嵌入式 OpenAI 兼容提供程序，例如**工作伙伴**,**码友**、光标、克劳德·代码、艾德和克莱恩。因为`gemini-web2api`本机代理 OpenAI 函数调用（`tools`/`tool_calls`），代理可以自主检查和编辑本地文件。

配置 WorkBuddy 通过`~/.workbuddy/models.json`（或者`%USERPROFILE%\.workbuddy\models.json`在 Windows 上）：

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

#### 让 Argon 在代理工具调用时表现得像 Hydrogen

部分 Gemini 4 模型（尤其是 **Argon**）会优先使用 Gemini 内置的 **Deep Research / 网页工具**，忽略注入的 `# Tool Use` 提示，导致 WorkBuddy 收不到可本地执行的 `tool_call`。**Hydrogen** 则能稳定输出预期格式：

````text
```tool_call
{"name": "Bash", "arguments": {"command": "gh repo clone ..."}}
```
````

如果希望在 WorkBuddy 中保留 `gemini-4-argon` 模型 ID，但让带工具的回合实际走 Hydrogen，可在 `config.json` 中设置：

```json
{
  "tool_model": "gemini-4-hydrogen"
}
```

只有声明了工具（且未设置 `tool_choice: "none"`）的请求会被重定向，普通聊天请求仍使用原模型。请求后缀 `@new`、`@continue`、`@think=N` 会被保留。触发重定向时会返回 `X-Tool-Model-Override: true` 响应头并打印日志；`X-Resolved-Model` 显示实际使用的模型。

也可以不修改配置文件，直接用启动参数或环境变量：

```bash
./gemini-web2api --tool-model gemini-4-hydrogen
# 或
GEMINI_TOOL_MODEL=gemini-4-hydrogen ./gemini-web2api
```

工具提示中也会明确要求：本地文件、shell 命令、GitHub 仓库和邮件必须通过输出 `tool_call` 块交给本地客户端执行，禁止用 Gemini 内置的网页搜索 / Deep Research 来回答。

## 配置

创造`config.json`在同一目录中：

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

什么时候`api_keys`是空的`[]`，身份验证已禁用。设置按键后，`/v1/*`端点需要`Authorization: Bearer <key>`。什么时候`bridge_token`已设定，`/bridge/*`端点（扩展 cookie 同步、桥接控制）需要`X-Bridge-Token`header — 在扩展弹出窗口的服务器连接部分设置相同的标记。

## 代理人

如果您无法访问`gemini.google.com`直接配置代理：

**方法一：CLI 参数**

```bash
# Linux / macOS
./gemini-web2api --proxy http://127.0.0.1:7890

# Windows (PowerShell)
.\gemini-web2api.exe --proxy http://127.0.0.1:7890
```

**方法2：config.json**

```json
{ "proxy": "http://127.0.0.1:7890" }
```

**方法三：环境变量**

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

## 局限性

* **不是真正的 Pro/Ultra**：没有付费订阅 cookie，`gemini-3.1-pro`路由到相同的 Flash 模型。 “Pro”标签是 UI 首选项，而不是后端模型开关。
* **仅单圈**：每个请求都是一个独立的对话。通过在提示中包含先前的消息来模拟多轮上下文。
* **速率限制**：Google 可能会限制高频请求。服务器会自动重试，但持续的大量使用可能会被阻止。

## 执照

我的许可证。
