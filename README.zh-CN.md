<!-- markdown-translator:970878840a7249d834bf61a728186dbb88958a487e802d710316cb4563e85218 -->
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

专为驱动工具、连接器、技能和 MCP 服务器的代理运行时而构建（WorkBuddy、Claude 式代理、OpenAI SDK）：

|特色 |行为 |
| --- | --- |
|`GET /v1/models`|每个别名都带有`capabilities`(`tools`,`connectors`,`mcp`,`vision`,`attachments`,`json_mode`,`streaming`),`context_window`,`premium`和`research_prone`标志，以便客户端可以在启用功能之前进行过滤 |
|`GET /v1/models/{id}`|代理运行时使用单模型探针 (200/404) 来验证配置的 ID |
|`parallel_tool_calls: false`|提示中强制执行：正好一`tool_call`每回合块|
|重复/无名工具|合并的 MCP + 连接器集按名称进行重复数据删除；内置声明，例如`{"type":"web_search"}`（无函数名称）被跳过，而不是泄漏无名条目 |
| MCP 工具名称 |命名空间名称 (`mcp__server__tool`,`server.tool`）原封不动地传递——它们是由你的客户执行的，而不是由 Gemini |
|技能 |长的`system`-仅支持提示（技能只是指令包）；`/v1/responses`并完全支持`/v1/chat/completions`|

### 优质模型永远不会陷入深度研究

氖气、氢、碳和氩被标记`research_prone`，因为 Gemini UI 很乐意将他们推入深度研究——一种仅读取公共网络的模式，**无法运行您的工具、连接器或 MCP 服务器**。和`prevent_deep_research`（默认`true`）对这些模型之一的启用工具的请求：

* 删除上游研究/浏览工具（`google_search`,`web_search`,`web_search_preview`,`deep_research`,`url_context`,`browse`，...）同时保留你真正的工具；
* 折叠一个`EXECUTION MODE`线进入`# Tool Use`序言：深度研究/内置搜索已禁用，有关机器、存储库、文件或应用程序的事实必须来自`tool_call`，并且丢失的数据必须通过工具获取而不是研究；
* 回答与`X-Deep-Research: disabled`和日志`Execution-mode guard applied for premium model "gemini-4-neon" …`.

```jsonc
{ "prevent_deep_research": true, "tool_model": "gemini-3.8-flash" }
```

`tool_model`当您希望支持工具的请求在更便宜、可靠的工具调用程序上运行时，无论客户要求什么模型，仍然会获胜。

## 对真实企业日志中的确切故障进行故障排除

**`context-file upload rejected (HTTP 400): One or more required fields are missing.`**— 小部件的`widgetAddContextFile`需要一个`configId`，并且没有捕获的上传模板或`discovery_engine_config_id`服务器只能导出 URL，而不能导出字段。`gemini-web2api`不再消耗往返行程：它预先拒绝

```
cannot attach "code_context.txt": the Discovery Engine widget configId is unknown and the widget rejects uploads without it —
send one message in the Gemini Enterprise tab with the extension enabled (the request is captured automatically), or set "discovery_engine_config_id"
```

初创公司的横幅也说了同样的事情，而不是承诺后备方案。当 400 确实返回时，错误现在会列出实际发送的字段（`sent fields: addContextFileRequest{fileContents, fileName, mimeType}, additionalParams, configId`），因此缺失的字段是可见的而不是猜测的。

**`modelId=<none>`/“仍然回答为自动”**—`discovery_engine_models`是空的，所以没有发送任何内容。两种出路：

1. **自动的：**&#x5728;启用扩展的情况下，在 Gemini Enterprise 选项卡中发送一条消息。它自己的`widgetStreamAssist`请求被捕获，租户的真实信息`assistGenerationConfig.modelId`（加`configId`和`session`) 然后被重用——日志变成`Enterprise: calling the Discovery Engine widgetStreamAssist API (files=0, bearer=false, requested="gemini-4-hydrogen", modelId="gemini-2.5-pro", source=captured)`和`/v1/model-info`报告`"source": "captured"`.
2. **明确：**&#x6620;射别名（请参阅上面的部分）以进行精确控制。

`source=captured`意味着“现在正在发送租户有效的模型，但它是选项卡选择的任何模型”——如果您想要特定的映射，请设置一个映射。之前打印的日志`model="gemini-4-hydrogen"`，这意味着别名已发送到上游；从来没有。

### 验证诊断端点

`/v1/*`受保护`api_keys`。这些形式中的任何一种都有效——`invalid api key`现在在响应正文中告诉您：

```bash
KEY=your-key-from-config
curl -H "Authorization: Bearer $KEY" http://localhost:8081/v1/model-info?model=gemini-4-hydrogen
curl -H "x-api-key: $KEY"            http://localhost:8081/v1/model-info?model=gemini-4-hydrogen
curl "http://localhost:8081/v1/model-info?model=gemini-4-hydrogen&key=$KEY"
```

### 发现租户模型 ID（无需 DevTools）

如果直接模式应该命名模型但不知道租户 ID，`gemini-web2api`现在**询问租户而不​​是猜测**: 一个最小的`widgetStreamAssist`调用携带请求的别名作为`assistGenerationConfig.modelId`。如果租户接受，则会验证 ID，并将每个别名缓存在`enterprise_models.json`，并从那时起使用：

```
Gemini Enterprise: tenant accepted model id "gemini-4-hydrogen" for "gemini-4-hydrogen" — cached in enterprise_models.json, direct mode will now select it
```

如果租户拒绝，则在此过程中不会再次探测别名，并且请求将在没有模型 ID（租户默认值）的情况下继续，而不是失败：

```
Gemini Enterprise: tenant does not accept "gemini-4-neon" as a model id (the tenant rejected model id "gemini-4-neon" (HTTP 400) …) — falling back to the tenant's auto/default model
```

控制它与`discovery_engine_probe_models`（默认`true`），或者显式运行探针并查看确切的上游答案：

```bash
curl -X POST localhost:8081/v1/model-probe \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-4-hydrogen","candidate":"gemini-4-hydrogen"}'
# {"ok":true,"model":"gemini-4-hydrogen","model_id":"gemini-4-hydrogen","source":"learned",…}
```

一次尝试一位候选人（`"candidate":"gemini-2.5-flash"`) 直至其中一项被接受；每一次成功都会被铭记。决议顺序为：`discovery_engine_models`→ 租户 ID 直通 →`discovery_engine_default_model_id`→**已学习（探针验证）**→ 从选项卡捕获 → 租户默认值。

### 使选择器具有权威性的路由规则

在 Gemini Enterprise 源上，有两种方法可以获得您所需的模型：

1. **直接模式**—`assistGenerationConfig.modelId`在小部件请求中。仅当租户 ID 已知时（a`discovery_engine_models`映射、直通租户 ID、配置的默认值或从选项卡捕获的请求中获知的 ID）。
2. **桥接模式**— 扩展程序单击选项卡选择器中的模型。这始终适用于选择器实际显示的模型。

所以路由现在是模型感知的：**如果直接模式无法命名所请求的车型，则该请求将通过桥接器，而不是由租户的汽车模型默默地应答。**&#x8FD9;就是曾经产生“我选择了氢气，它仍然显示自动”的情况：

```
Keeping the browser bridge: direct mode has no tenant model id for "gemini-4-hydrogen",
so only the tab's model picker can select it (map discovery_engine_models to make direct mode honour it)
```

响应携带`X-Model-Route: bridge-picker`， 和`/v1/model-info`报告`"source": "bridge-picker"`和`selection_live: true`.

有两个值得了解的后果：

* **该选项卡必须保持打开和唤醒状态。**&#x43;hrome 的内存保护程序会冻结后台选项卡；冻结的选项卡被报告为已断开连接，桥接器不可用，并且转弯回退到直接模式 → 租户自动。日志明确指出（`MODEL SELECTION WARNING: … no usable browser tab …`）， 和`/bridge/status`节目`frozen: true`.
* **映射的别名永远不会触及该选项卡。**&#x4E00;次`discovery_engine_models`具有 id，直接模式根本不需要浏览器即可处理请求 - 这也是附件的原因（`files=N`） 可能的。

## “我选择了 Hydrogen，但它回答为 Auto” — 诊断模型选择

根据路线的不同，在三个不同的地方决定了回答的模型，并且每个模型都可以默默地后退。`GET /v1/model-info?model=gemini-4-hydrogen`报告目前适用的一项：

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

`upstream.source`准确地告诉您发生了什么：

|`source`|意义|
| --- | --- |
|`mapping`|`discovery_engine_models`匹配 — 租户用映射模型回答 |
|`passthrough`|客户直接要求提供租户 ID（`gemini-2.5-pro`) |
|`configured-default`|别名未映射，`discovery_engine_default_model_id`二手 |
|`tenant-default`|**没有选择任何内容 - 租户决定（通常是“自动”）**|
|`alias`|消费者来源：别名本身路由模型 |
|`bridge-picker`|没有租户 ID 的企业：请求通过浏览器桥，该桥在选项卡的选择器中选择模型 |
|`captured`|没有映射的企业：租户`modelId`从选项卡捕获的请求获悉被重用 |
|`(tab picker)`（警告）|需要模型选择器，但没有连接选项卡 - 转向回落到租户的汽车模型 |

每个聊天回复在标题中都带有相同的结论，因此您永远不必相信答案文本（`Gemini`回复中的自我描述是生成的散文，而不是路由报告）：

```
X-Resolved-Model: gemini-4-hydrogen
X-Upstream-Model: tenant-default          # or the real tenant model id
X-Upstream-Model-Source: tenant-default   # mapping | passthrough | configured-default | alias
X-Model-Selection: ignored-by-tenant      # live | ignored-by-tenant
```

**针对企业租户的修复：**&#x522B;名必须映射到租户实际提供的模型 ID。从选项卡本身读取`widgetStreamAssist`要求 （`assistGenerationConfig.modelId`）并配置：

```jsonc
{ "discovery_engine_models": { "gemini-4-hydrogen": "<tenant model id>", "gemini-4-neon": "<tenant model id>" } }
```

如果租户的选择器根本没有列出氢/氖，则没有映射可以发明它 - 将别名映射到租户提供的最接近的模型（例如其 4 Argon 条目），或使用那里存在的模型 ID。

**桥接模式：**&#x6269;展程序单击选项卡模型下拉列表中的条目，然后报告结果。当选择器中缺少请求的模型时，您现在得到的不是静默的自动应答：

```
MODEL SELECTION WARNING: the extension could not select "gemini-4-hydrogen" in the tab (picker currently shows "Auto") — this turn and later turns are answered by whatever the tab has open …
```

和`/bridge/status`长出一个`model_selection: { requested, applied, ok }`块，因此最后一次切换尝试始终是可检查的。

## 可用型号

|型号|描述 |输出|
| -------------------------------- | ------------------------------------------------------------------ | ---------------- |
|`gemini-4-neon`|适用于复杂工作的最新前沿模型（需要 Enterprise/Pro） |**~20k+ 字符**|
|`neon`,`gemini-4-neon-thinking`| Gemini-4-neon 的别名 |**~20k+ 字符**|
|`gemini-4-argon`|复杂任务的前沿模型（需要 Pro）|**~20k+ 字符**|
|`argon`,`gemini-4`| Gemini-4-argon 的别名 |**~20k+ 字符**|
|`gemini-4-hydrogen`|高速前沿执行模型|**~20k+ 字符**|
|`hydrogen`| Gemini-4-氢的别名 |**~20k+ 字符**|
|`gemini-3.8-flash`|最新全能机型（Gemini 3.8 Flash）|约 12k 个字符 |
|`gemini-3.7-flash`|全能机型（Gemini 3.7 Flash）|约 12k 个字符 |
|`gemini-3.6-flash`|全能机型（Gemini 3.6 Flash）|约 12k 个字符 |
|`gemini-3.5-flash`| gemini-3.6-flash 的别名 |约 12k 个字符 |
|`gemini-3.5-flash-thinking`|延伸思维，最长产出 |**约 20,000 个字符**|
|`gemini-3.5-flash-thinking-lite`|适应性思维深度|约 15,000 个字符 |
|`gemini-3.1-pro`|高级数学和代码（需要 Pro cookie）|约 12k 个字符 |
|`gemini-2.5-pro`|双子座2.5 Pro |约 12k 个字符 |
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

1. **每个请求的型号后缀**— 追加`@new`（或者`@fresh`/`@newchat`）强制进行新的聊天，`@continue`（或者`@cont`）以继续附加到打开的聊天中。适用于**任何**型号名称或别名，并与`@think=N`以任意顺序：
   ```text
   gemini-4-hydrogen@new                # this request starts a brand-new chat
   gemini-4-carbon@continue             # keep the open chat (incl. Gemini memory)
   gemini-4-carbon@think=0@continue     # deep thinking, keep the open chat
   carbon@new                           # aliases work too
   ```
2. **扩展弹出“聊天模式”选择器**—*自动（推荐）*/*总是有新的聊天*/*继续打开聊天*。选择被推送到服务器（`POST /bridge/chatmode`）并持续存在直到发生改变。和*继续打开聊天*，首先单击 Gemini 历史记录侧栏中的任何旧聊天 - API 轮流然后继续添加到该对话。
3. **自动启发式（默认）**— 从多轮客户端记录到新的单消息请求的清晰过渡启动了新的浏览器聊天。当客户端提供稳定的对话 ID 时，将使用该 ID：相同的 ID 继续现有的 Gemini 聊天，而不同的 ID 则开始干净的聊天。

代理识别`conversation_id`,`chat_id`,`thread_id`， 和`session_id`（也在`metadata`），对应的`X-...-ID`标头和响应 API`previous_response_id`链。每次 WorkBuddy 聊天时 ID 必须保持不变，并在用户开始新聊天时更改。如果客户端每个请求仅发送一条用户消息并且不提供稳定的 ID，则无法区分后续聊天和新聊天；自动有利于连续性，而不是根据提示打开一个 Gemini 聊天。在这种情况下，使用*总是有新的聊天*/`@new`用于故意重置，或配置客户端发送其对话 ID。`@continue`或者*继续打开聊天*禁用自动重置。当客户端发送历史记录时，只要选项卡没有匹配的上下文，代理就会完整地重播它。

#### 直接模式也有同样的问题——每个客户聊天一个会话

没有浏览器选项卡，连续性就存在于发现引擎中**会议**.`/v1/model-info`再加上一条日志行可以告诉您哪一个正在发挥作用：

```
Enterprise: continuing in assistant session 8f1c (reused for every turn of this conversation)
Enterprise: calling the Discovery Engine widgetStreamAssist API (files=0, bearer=false, session=8f1c, requested="gemini-4-hydrogen", modelId="gemini-4-hydrogen", source=learned)
```

**每个客户聊天一次会话。**&#x5F53;客户端提供稳定的会话ID时，代理将其用作会话密钥；否则它会回退到第一条用户消息和记录历史记录：

* 使用相同的 ID 继续聊天 → 相同的会话，因此线程保留其内存；
* **使用更改后的 ID 打开新聊天→新会话**，因此它永远不会继承前一个线程的内存；
* 如果没有 ID，从多轮转录到单消息请求的重置仍然是传统的回退信号，但对于无状态客户端来说可能不明确；
* 使用其 ID 恢复较早的聊天→再次该聊天自己的会话，而不是最近的会话；
* 只发送一条没有历史记录或 ID 的消息的客户端会保留一个会话，因为没有任何东西可以区分新聊天和后续聊天。

```
Enterprise: continuing in assistant session 8f1c for conversation 4c1a9ba2
Enterprise: switching back to the session owned by conversation 4c1a9ba2
```

会话曾经是每个进程一个（`widgetCreateSession`）由每个聊天共享。重新启动或 cookie/JWT 重置会启动一个新的记录 - 记录不会丢失，因为您的客户端重新发送其历史记录，但租户的线程是新的（并且昨天上传的文件 ID 消失了）。如果租户拒绝创建会话，您会得到相反的症状，在日志中详细说明：

```
Gemini Enterprise: could not create an assistant session (HTTP 403 …) — the tenant will show every request as a new conversation
```

会话也可能在租户端消失——过期、引擎重建、另一个后端 Pod：

```
HTTP 404 Session with name "projects/…/sessions/10740851950609687161" does not exist.
```

这曾经导致整个回合失败。现在会话被删除，请求在没有会话的情况下重试一次，并为下一轮创建一个新会话：

```
Gemini Enterprise: the tenant no longer knows session 1074085195060968716 (HTTP 404) — retrying without a session; a fresh one is created for the next turn
```

404 就是*不是*关于会话（错误的端点，错误的 configId）仍然正常失败 - 恢复是故意缩小的。

固定特定对话`"discovery_engine_session": "collections/…/sessions/…"`在`config.json`.

### 模型检查头

每个 API 响应（JSON 和流式 SSE）都包含指示实际模型路由的 HTTP 响应标头：

* `X-Resolved-Model`：实际路由到的后端模型标识符（例如`gemini-4-argon`).
* `X-Model-Fallback: true`：每当请求未映射的模型并回退到默认模型时都包含在内。

## 高级功能（独家）

### 1.动态模型发现和上游路由头

当使用帐户 cookie 进行身份验证时，代理会动态查询 Gemini 的`otAQ7b`启动期间进行 RPC 以发现可用模型和帐户层容量。它附加所需的上游路由标头（`x-goog-ext-525001261-jspb`）匹配 Web 应用程序行为，允许 Gemini 4 Argon 等模型正确路由。

发现的模型已匹配**按显示名称**（例如`gemini-4-carbon`→ 发现的“4 Carbon”条目），因此帐户选择器中的每个模型都有自己的真实上游 ID - 氩/碳/氢不再共享一个静态标头。在**桥接模式**该扩展程序会在 Web 模型下拉列表中选择确切的条目（包括版本化条目，例如`3.8 Flash`与`3.5 Flash`和`2.5 Pro`).

### 2. 视觉/多模式上传

与忽略图像的 Python 版本不同，此 Go 端口主动拦截`image_url`有效负载（base64 编码和外部 HTTP URL）。
它启动后台 Scotty 可恢复上传会话，将 base64 图像转换为 Google 内部 WIZ blob，并将 WIZ 图像引用直接传递给 Gemini。
只需发送 OpenAI 标准多模式有效负载，它就会起作用！

### 3. 图像和视频生成

Gemini web 可以生成图像 (Nano Banana) 和视频 (Veo)，但将它们返回为*媒体*，而不是聊天文本 - 文本管道会丢弃二进制部分，因此一个简单的`/v1/chat/completions`调用图像模型可以获得描述。这两个端点驱动媒体路径并将结果返回到 OpenAI 形状的信封中：

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

工作原理：

* 提示已被包装，因此 Gemini 使用自己的媒体工具并使用渲染的资产（或明确的`IMAGE GENERATION UNAVAILABLE`标记而不是一篇看似合理的文章）。
* 媒体被认可**按内容**— 数据 URI、base64 JSON 字符串（PNG/JPEG/GIF/WebP/MP4/WebM）内的文件头魔法以及 Google 主机上的资产 URL — 因此相同的提取器适用于消费者`StreamGenerate`, 发现引擎`streamAssist`，和纯文本传输类似。一切都不是捏造的：没有资产就意味着`502`用模特自己的话来说`error.text`.
* `response_format: "b64_json"`内联字节；仅从 Google 资产主机获取（从模型输出中抓取的 URL 永远不会成为任意服务器端请求）。
* 图像/视频模型名称（`nano-banana`,`veo-3`，...）不是注册表中的聊天模型，因此未知名称会使用默认传输设置转发到上游，并由租户自己的模型 ID 规则解析（`/v1/model-probe`仍然适用于那些）。

#### 如果客户有自己的图像工具（WorkBuddy 积分）

工作伙伴船舶`generate_image`/`create_video`风格的工具。当模型发出`tool_call`对于其中之一，**客户**渲染媒体并花费自己的积分——双子座永远无法渲染它。

因此，代理在模型看到这些工具之前从请求中删除它们，并告诉模型使用自己的媒体工具进行渲染：

* 强制执行于**每一个**入口点：`/v1/chat/completions`,`/v1/responses`（WorkBuddy 代理使用的表面）和`/v1beta/models/{model}:generateContent`- 仅覆盖其中之一，使该工具在其他工具上发挥作用
* 名称匹配（`generate_image`,`image_generation`,`create_image`,`text_to_image`,`nano_banana`,`imagen`,`generate_video`,`create_video`,`text_to_video`,`veo`和工作伙伴的`ImageGen`，加上驼峰命名法/`-`/`_`变体和 MCP 命名空间形式）
* 响应头`X-Media-Tools-Intercepted: generate_image`和日志行`Client offered its own media tool(s) [generate_image] — removed from this request so Gemini renders the media itself (no client credits spent)`
* 每个媒体请求都会记录其结果，因此答案绝不会含糊：`MEDIA: "gemini-4-hydrogen" answered with 1 asset(s)`相对`MEDIA: "gemini-4-hydrogen" answered with text but no image/video in the payload — the tenant/model did not render it`
* 渲染的资源返回并附加到回复中：`![generated image](data:image/png;base64,…)`对于内联字节（上限为 8 MB）或`![generated image](https://…)`对于链接，`[generated video](…)`用于视频

其他一切都保持正常工作：普通工具（文件、外壳、连接器）保持不变，`tool_choice`名为已删除工具的名称会回落到`auto`，并且不提供任何媒体工具的请求根本不会捕获任何内容。

如果日志显示**不** `Client offered its own media tool(s)`行用于图像提示，客户端在根本不调用此 API 的情况下生成它 - 该开关位于 WorkBuddy 的设置中，而不是在这里。

### 4. 拒绝字段的附件上传

部分租客回答`HTTP 400 Invalid JSON payload received. Unknown name "session" at 'add_context_file_request': Cannot find field.`小部件命名了它拒绝的确切字段，因此上传会删除该字段，重试相同的请求，并记住该过程接受的形状 - 不再因为一个可选字段被拒绝而每次都内联 20 KB 提示：

```
Gemini Enterprise: the widget does not accept "session" in addContextFileRequest — retrying without it
Enterprise context file "code_context.txt" uploaded as text/plain (20 KB, fileId=… session=…)
```

如果重试仍然失败，附件会像以前一样内联，并且日志会说明原因。

### 5. 模型实际可以读取的指令

仅当租户的文件读取器将其主体交给模型时，上下文文件才是可读的 - 到目前为止所看到的租户上还没有，并且模型会回答您自己的指令文件：

> *“我无法查看您的最新回复，因为此对话的上下文文件 (`code_context.txt`) 已超出内联显示限制...请打开新聊天。”*

该答复意味着说明位于文件中，而不是其他地方。所以`enterprise_mirror_text_inline`（默认**真的**) 使每个文本附件都可以旅行**两个都**方式：作为租户的上下文文件上传，并写入模型肯定会读取的提示中。

```
Enterprise attachments: 1 uploaded as context files, 1 inlined (1 of them also written into the prompt so the model can read them)
```

出于同样的原因，超大提示不再溢出到`code_context.txt`当镜像打开时：仅指向不可读文件的提示没有留下任何说明。放`"enterprise_mirror_text_inline": false`如果您的租户确实提供文件正文，则恢复旧行为（小提示，附件中的所有内容）。

二进制附件（图像、PDF）永远不会被镜像——它们的字节不能作为文本内联，因此它们仍然依赖于上传传输。

保持指令内联有其自身的上限：Discovery Engine 拒绝过大的提示`INVALID_ARGUMENT / PROMPT_TOO_LARGE`，并且每个租户的上限有所不同。代理会学习它——重试第一个过大的转弯，并忽略最旧的转弯，并且在发送之前每个后面的转弯都会被修剪：

```
Direct Discovery Engine rejected the prompt as too large (305089 chars) — retrying with the oldest turns omitted (228816 chars)
Enterprise: prompt trimmed from 305089 to 114400 chars (oldest turns omitted) to stay under the upstream limit
```

修剪保留头部（系统提示、说明、附加文件序言）和尾部（最近的回合、当前问题），用一行通知替换中间，以便模型知道历史记录是部分的，并且永远不会剪切当前问题。放`"enterprise_max_prompt_chars"`跳过单次从失败中学习的往返过程。

有两条规则可以防止对话被破坏：

* **在修剪提示之前会话会被删除。**&#x4F1A;话自身的历史记录计入租户的限制，并且每次都会增加整个提示，因此长时间运行的会话最终会拒绝每个请求，无论提示有多小。如果拒绝，会话将被清除，并重试相同的（未修剪的）提示 - 客户端无论如何都会重新发送其历史记录：

  ```
  Direct Discovery Engine refused the prompt as too large (HTTP 400, 286688 chars) while a session was attached — the session's accumulated history is what became too large, so retrying in a fresh session instead of trimming the prompt
  ```

* **低于 8 KB 什么也学不到。**&#x62D2;绝小提示与提示大小无关，因此它永远不会降低记住的上限 - 这就是早期版本如何将自己减少到 247 个字符的提示并每次都失败。相反，会报告这样的拒绝：

  ```
  Direct Discovery Engine refused a 247-char prompt as too large and it cannot be the prompt size — check the tenant's own limits (session history, attachments, model) rather than the request
  ```

### 6. 租户工具、MCP 连接器和工具规格

Gemini Enterprise 运行的工具**助手**拥有（包括从 MCP 连接器导入的操作），而不是代理声明的工具。`StreamAssistRequest.ToolsSpec`根本没有任意工具的注册表：它包含 Vertex AI 搜索、网络接地、图像和视频规范。因此，当模型说“我没有 REA 工具”时，答案就在租户中，而不是在租户中`/v1/chat/completions`:

* MCP 数据存储必须是**积极的**并链接到同一个应用程序的小部件`configId`指向；
* 它是进口的**行动**必须在数据存储上启用（它们开始禁用）；
* 聊天只能看到它开始时的操作 - a*新聊天*选择新启用的。

代理所控制的是`toolsSpec`它要求并每次都会记录下来：

```
Enterprise: toolsSpec from default: {"toolRegistry":"default_tool_registry"}
```

三种设置方法：

|`discovery_engine_tools_spec`|效果|
|---|---|
|取消设置 |来自捕获模板的toolsSpec，否则捕获上面的默认值 |
|`"none"`|该字段被完全省略——助手自己的配置决定 |
|一个 JSON 对象 |逐字发送；粘贴真实应用程序在其操作开启后发送的内容 |

`"none"`A/B 测试是否值得首先尝试：如果固定工具注册表隐藏了租户的自定义操作，则省略该字段会将它们带回来 - 并且日志行会告诉您使用了哪种形状。

在提示方面，工具序言现在表示主机端工具（MCP 操作、文档生成器）是一个允许的、单独的集合，因此仅看到声明的客户端工具的模型并不能断定它没有其他工具。

### 7. TLS 模拟（绕过 WAF）

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
* **超大提示/工具结果溢出（`bridge_spillover_chars: 18000`)**：提示或巨大`read_file`超过 18 KB 的工具输出会自动上传为`.txt`附件而不是被猛击到网络输入框中，从而防止 UI 截断和重复重新读取。**当会话 cookie 同步且正常时，过大的提示会完全跳过浏览器**并通过直接 cookie 模式提供服务（完整的提示符内联到批处理执行主体中 - 没有浏览器文件上传，这会在某些 Gemini Enterprise 前端上停止）。仅最新用户消息引用本地文件的请求**实际上存在于服务器磁盘上**留在桥上，以便可以附加文件 - 仅在提示中提及路径（就像编码代理经常做的那样）永远不会将请求固定到桥上。
* **Delta 转弯上下文 (`bridge_delta_context: true`)**：同一打开的 Gemini 选项卡中的后续轮次仅发送新附加的用户/工具消息，而不是每次都重新粘贴完整的对话历史记录和工具架构（刷新或新聊天时自动重置）。
* **冻结/卡住选项卡故障转移 (`bridge_no_content_timeout_sec: 120`)**：如果 Chrome 在中途冻结选项卡（内存保护程序），则正在进行的请求将立即中止并通过直接 cookie 模式重试，而不是挂起直到客户端超时。只产生保活（无内容）时间超过此限制的回合也会进行故障转移；主动生成的选项卡 (`"generating"`阶段）永远不会被这个上限切断。设置为`0`禁用。
* **无人认领的请求故障转移（`bridge_pickup_timeout_sec: 45`)**：如果此窗口内的任何浏览器选项卡均未拾取排队的请求 - 停滞的扩展轮询循环或睡眠的 Chrome MV3 服务工作线程的签名（选项卡心跳但从不轮询） - 请求会快速失败并通过直接 cookie 模式重试。一旦选项卡声明请求，该计时器就会将其交给无内容看门狗。设置为`0`禁用。
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
  "bridge_delta_context": true,
  "bridge_no_content_timeout_sec": 120,
  "bridge_pickup_timeout_sec": 45
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

**浏览器同步会话（cookie 模式）：**&#x58;SRF 令牌是特定于来源的。当扩展程序同步 cookie 时，它​​还会报告其从中获取的选项卡的来源；服务器将其保存到 cookie 文件中，并将其用于所有直接 cookie 模式请求（发现 + 生成 + 页面令牌获取 + BL 刷新） - 它优先于`base_url`。所以如果你曾经打开过`gemini.google.com`配置后`vertexaisearch.cloud.google.com`，服务器遵循选项卡，而不是过时的配置。这`GET /bridge/cookies`状态现在报告两者`synced_origin`和`configured_base_url`;不匹配（`configured=gemini.google.com`+`synced=vertexaisearch...`）正是每个批处理上的上游 HTTP 400 的含义 - 从右侧选项卡同步或修复`base_url`.

**Bard RPC 自动检测：**&#x4E00;些 Gemini Enterprise 部署根本不为 Bard Web RPC 提供服务（批量执行回答 HTTP 404 — 该应用程序使用 Discovery Engine`streamAssist`应用程序编程接口）。服务器在第一个探测器上检测到这一点，并将其锁定（`BardUnsupported`），并通过**浏览器桥**— 那里的工作路径 — 而不是重试直接模式进入 502。当 cookie 同步从不同的源到达时，锁存器会重置（并且发现重新探测退避也会随之重置）；同源周期性同步保持锁存。 404 类错误永远不会重试，并且`Generate`/`GenerateStream`在闩锁上快速失败，无需花费上游尝试。当标签消失时**和**直接模式无法使用，API 回答`503 bridge_offline`并显示一条消息，告诉您在 Chrome 中打开 Gemini。

**Discovery Engine重播（企业直接模式）：**&#x6269;展程序的网络钩子捕获应用程序自己的网络钩子`streamAssist`每当选项卡生成时，请求（URL，标题，正文），将提交的提示拼接出来`__W2A_PROMPT__`标记，并将模板推送到`POST /bridge/reqtemplate`（保留在 cookie 文件旁边）。在锁定的 Enterprise 源上，服务器会使用新的 cookie 和新的提示重播该模板 - 因此`/v1/chat/completions`作品**浏览器关闭时**。模板携带捕获的`Authorization`（通常是 SAPISIDHASH）；如果它已过期，服务器会使用新计算的 SAPISIDHASH 值进行重试，如果失败，则会告诉您通过打开的选项卡发送一条提示来刷新模板。检查状态`GET /bridge/reqtemplate`.

**企业选型：**&#x47;emini Enterprise 使用其助手配置的型号进行应答，除非请求携带`assistGenerationConfig.modelId`.`gemini-web2api`现在发送它，所以客户要求`gemini-4-hydrogen`不再默默降级为租户的“auto”：

```jsonc
{
  "discovery_engine_models": { "gemini-4-hydrogen": "gemini-2.5-flash", "argon": "gemini-2.5-pro" },
  "discovery_engine_default_model_id": ""   // optional fallback for unmapped names
}
```

值必须是您的租户接受的模型 ID（查看`assistGenerationConfig.modelId`在选项卡自己的`widgetStreamAssist`要求）。已经看起来像上游 ID 的名称（`gemini-2.5-pro`,`gemini-3-pro`) 未受影响地通过；本地别名（`argon`/`hydrogen`/`carbon`）永远不会被通过，因为租户不认识它们。捕获的模板重播得到相同的处理：重写到模板中的 modelId。

**Enterprise 上的超大提示会跳过浏览器溢出：**&#x8FD9;`.txt`溢出的存在是为了躲避消费者 Gemini Web 编辑器的限制。在 Discovery Engine 源上，提示被内联到`streamAssist`JSON 主体，因此超大请求现在直接进入直接模式，而不是等待实时选项卡（这就是产生的结果）`Bridge request failed before stream start: context canceled`+ 502）。当直接模式根本没有传输时，仍然使用桥接器。

**Widget JWT auth（这就是修复 Enterprise 502s 的方法）：**&#x53D1;现引擎小部件端点（`widgetStreamAssist`,`widgetCreateSession`,`widgetAddContextFile`） 在`vertexaisearch.cloud.google.com`部署做**不是**接受消费者`SAPISIDHASH`标头。该应用程序创建了一个短暂的 HS256 JWT：

1. `GET https://vertexaisearch.cloud.google.com/auth/getoxsrf?csesidx=<id>`和你的饼干 →`{"xsrfToken":"<base64url HMAC key>","keyId":"<kid>"}`
2. 符号`{"iss":<origin>,"aud":"https://content-us-discoveryengine.googleapis.com","sub":"csesidx/<id>","iat","exp":+300,"nbf"}`用那把钥匙，`kid`在标题中
3. 称呼`https://content-<location>-discoveryengine.googleapis.com/v1alpha/locations/<location>/widgetCreateSession`， 然后`.../widgetStreamAssist`和`Authorization: Bearer <jwt>`,`Origin/Referer: <your enterprise origin>`和`additionalParams: {"token":"-","origin":"ORIGIN_UNSPECIFIED"}`

`gemini-web2api`一旦它知道了，所有这些都会自动完&#x6210;**`csesidx`**（Gemini Enterprise 选项卡 URL 中的对话/配置序列 ID）。扩展程序在 cookie 同步期间收集它（`?csesidx=…`），所以普通的cookie同步就足够了；否则设置`discovery_engine_csesidx`.`discovery_engine_config_id`也是必需的——它是小部件`configId`应用程序发送（当您在选项卡中附加文件时自动捕获）。`discovery_engine_location`（默认`us`，或从您的端点派生）选择区域小部件主机。

没有一个`csesidx`服务器回退到旧的 cookie 验证`?key=`端点，这正是答案的路径`502`/`no answer text`在大多数租户上 - 启动横幅报告正在使用哪种传输（`Widget auth: widget JWT (csesidx=…)`与`cookie fallback`).

**编辑长代码文件而不使用获取循环：**&#x5F53;提示引用位于或以上的源文件时`enterprise_auto_attach_min_bytes`（默认：`bridge_large_code_upload_bytes`，8 KB），文件已附加，提示会显示简短的清单而不是正文：

```
# Attached workspace files (already loaded — do not re-read)
- /work/app/handlers.ts — 1,842 lines, 78 KB — attached as context file "handlers.ts"
These attachments are the current on-disk version as of this request. Do not call read_file, grep or shell cat to
fetch their contents again ... Make the change with your edit/write tool using a targeted patch (str_replace /
line range) — the updated file is re-attached automatically on the next turn, so no verification read is needed.
```

这消除了往返成本的两半：模型从不花费一圈`read_file`-ing 文件，并且它永远不必将整个文件作为工具参数（昂贵的输出）回显。 WorkBuddy 编写补丁后，**下一个**请求从磁盘重新读取文件并将新字节作为新版本附加，因此模型可以看到自己的编辑而无需验证读取。上传内容进行哈希缓存：每个文件一次上传*版本*，每转为零。

诚实范围：附件是只读上下文。实际的编辑仍然通过 WorkBuddy 自己的写入工具进行，但该方向是自由的，因为内容从模型移动到磁盘，永远不会回到 Gemini。

**图像的实际读取方式：** `widgetAddContextFile`当您将图片粘贴到其编辑器中时，Gemini Enterprise 应用程序会进行相同的调用 - 字节落在助手会话中，并且`streamAssist`引用它们`fileId`，因此多模式助手读取图像本身（视觉标记），而不是文件名。`gemini-web2api`使用真实的 MIME 类型逐字节上传图像（`image/png`,`image/jpeg`，...）并且从不将它们转换为文本：`IsTextLike()`对于图像类型返回 false，因此图像永远不能作为垃圾默默内联。上传日志证明了形状：`Enterprise context file "diagram.png" uploaded as image/png (240 KB, fileId=…)`.

图像通过以下三种途径之一到达：OpenAI`image_url`部分（数据 URL 或远程 URL，获取然后上传），Google`inlineData`部分以及提示中引用的本地路径（`.png/.jpg/.jpeg/.webp/.gif/.bmp`是自动附加的）。如果租户拒绝上传，请求将失败并出现可操作错误，而不是删除图像。

**反封禁附件策略（大文件、图片、PDF/Office）：**&#x5728; Enterprise 上，提示已内联到`streamAssist`body，因此每次重新发送文件内容的代理正是触发速率限制的原因。通过可用的上传传输，服务器现在将该内容移动到上下文文件附件中：

* 文件从磁盘读取并附加按路径（PDF、Word、PowerPoint、图像、长源文件）引用的最新用户消息 - 模型以本机方式读取它们，包括图像；
* 一个过大的提示，或者其中的大的围栏代码块，被溢出到一个`.txt`附件并通过临时通知更换；
* 上传内容通过内容哈希进行缓存（`discovery_engine_upload_cache_sec`），所以同一个文件被上传一次，然后被引用`fileId`在稍后的回合中。

```jsonc
{
  "enterprise_auto_attach_local_files": true,   // attach referenced local files
  "enterprise_auto_attach_min_bytes": 0,        // 0 = reuse bridge_large_code_upload_bytes
  "enterprise_attach_large_prompt": true        // spill oversized prompts into a .txt
}
```

两个交换机都是无操作的，没有上传传输（没有捕获的模板，没有`discovery_engine_upload_endpoint`，没有小部件`csesidx`)：在这种情况下，内容保持内联，因此不会默默地删除任何内容。使用日志行验证`Enterprise: calling the Discovery Engine widgetStreamAssist API (files=1, bearer=true, model="…")`—`files=0`意味着没有附加任何东西。

**关闭浏览器时的文件附件（企业版）：**&#x6D88;费者 Gemini Web 通过以下方式上传附件`content-push.googleapis.com`管道并从 Bard RPC 有效负载引用它们。 Gemini Enterprise 没有这样的管道 - 该应用程序将文件附加到*助理会议*反而 （`widgetAddContextFile`→`fileId`， 然后`streamAssist`和`fileIds`+`session`),所以用cookie模式来应答`this Gemini Enterprise origin needs the browser bridge for image attachments`。这个差距已经弥合了：

1. **捕获一次（推荐，零配置）。**&#x52A0;载扩展后，在 Gemini Enterprise 选项卡中附加一个文件。 net-hook 记录上传请求，桥接器推送其模板化副本（文件名、MIME 类型和 Base64 内容替换为`__W2A_FILE_NAME__`/`__W2A_FILE_MIME__`/`__W2A_FILE_B64__`） 到`POST /bridge/uploadtemplate`;它被持久化为`discovery_upload_template.json`在 cookie 文件旁边。从那时起，服务器将使用新的 cookie 重播该请求**每一个**附件 — 浏览器可以保持关闭状态。检查一下`GET /bridge/uploadtemplate`(`{"have_template":true,...}`).
2. **配置端点。**&#x5982;果没有捕获的模板，请设置`discovery_engine_upload_endpoint`到`widgetAddContextFile`该选项卡的 DevTools Network 选项卡中的 URL。如果省略，服务器通过交换来派生它`widgetStreamAssist`→`widgetAddContextFile`在`discovery_engine_endpoint`.`discovery_engine_session`（例如`collections/default_collection/engines/my-engine/sessions/-`） 和`discovery_engine_config_id`固定会话/小部件配置；否则，两者都会从捕获的请求主体中读取。
3. **文本后备。**&#x5982;果没有可用的上传路径，则类似文本的附件（代码、JSON、CSV、Markdown、日志 - 每个最多 200 KB / 每个请求 400 KB）将内嵌到提示中并明确显示`--- Attached file: … ---`标记，因此编码代理仍然可以看到他们的文件。二进制附件（图像、PDF、Office 文档）返回可操作的错误，而不是静默删除。

上传的文件位于重播使用的同一会话中，因此后续轮流保留文件上下文。上传相同的字节**一次**：请求内的重复项共享一个`fileId`，并且上传内容通过内容哈希进行缓存`discovery_engine_upload_cache_sec`（默认1800，`0`禁用），因此每次重新发送相同存储库文件的代理循环会完全跳过上传往返过程。最多并行上传 4 个文件。

**保持选项卡处于活动状态：**&#x8BE5;扩展标记了每个 Gemini 选项卡`autoDiscardable: false`因此 Chrome 的内存保护程序永远不会丢弃它 - 丢弃的选项卡会默默地杀死网桥轮询/心跳循环。它还报告它发现已被丢弃的选项卡（在每个 cookie 同步期间并通过 30 秒的活动监视器对选项卡的轮询循环进行 ping 操作），并且内容脚本在页面卸载时报告其自身的死亡。在企业部署中，该选项卡是服务器到 Gemini 的唯一路径：在 Chrome 中保持打开状态。

**选项卡消失时的实际错误：**&#x4E00;旦选项卡实际声明了排队的工作，流式响应才会提交到 HTTP 200。如果没有在时间内收到请求`bridge_pickup_timeout_sec`（默认45秒），API应答`503 bridge_offline`一条消息告诉您打开/重新加载 Gemini 选项卡 - 而不是客户端呈现为卡住或空白完成的空 200 流。

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

#### 使氩气像氢气一样进行代理工具调用

一些 Gemini 4 型号（特别是**氩气**）更喜欢Gemini的内置**深入研究/网络工具**并忽略注入的`# Tool Use`提示，因此 WorkBuddy 永远不会收到`tool_call`在本地执行。**氢**可靠地发出预期的块：

````text
```tool_call
{"name": "Bash", "arguments": {"command": "gh repo clone ..."}}
```
````

如果你想保留`gemini-4-argon`在 WorkBuddy 中配置，但启用工具的轮流实际上在 Hydrogen 上运行，设置`tool_model`在`config.json`:

```json
{
  "tool_model": "gemini-4-hydrogen"
}
```

仅请求声明工具（并且不使用`tool_choice: "none"`) 被重定向；正常的聊天请求仍然使用请求的模型。每个请求的后缀，例如`@new`,`@continue`和`@think=N`被保留。响应头`X-Tool-Model-Override: true`以及服务器日志行显示覆盖何时被触发；`X-Resolved-Model`报告实际使用的模型。

也可以在不编辑文件的情况下应用该设置：

```bash
./gemini-web2api --tool-model gemini-4-hydrogen
# or
GEMINI_TOOL_MODEL=gemini-4-hydrogen ./gemini-web2api
```

注入的工具提示还告诉模型必须通过发出来处理本地文件、shell 命令、GitHub 存储库和电子邮件`tool_call`块，并明确禁止使用 Gemini 的内置网络搜索或深度研究来回答它们。

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
* **仅单圈**：每个请求都是一个独立的对话。通过在提示中包含先前的消息来模拟多轮上下文。 Gemini Enterprise 会话是每个进程一个且不会持久，因此第二天继续聊天依赖于客户端重新发送其历史记录（并且必须重新附加昨天的任何附件）。
* **媒体取决于帐户**:`/v1/images/generations`和`/v1/videos/generations`只能返回登录帐户的 Gemini web 允许生成的内容。没有图像/视频访问权限的租户使用文本进行应答，端点会准确报告该情况，而不是返回占位符。
* **速率限制**：Google 可能会限制高频请求。服务器会自动重试，但持续的大量使用可能会被阻止。

## 执照

我的许可证。
