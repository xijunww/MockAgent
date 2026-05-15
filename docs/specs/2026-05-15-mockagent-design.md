# MockAgent 设计文档

**日期**：2026-05-15
**状态**：已批准，进入实现阶段

## 1. 目标

构建一个独立的桌面应用 **MockAgent**：用户在任何前台应用中按住全局热键 `F2`，对着麦克风说话，松开热键后程序将这段音频通过腾讯云语音识别（ASR）转成文字，回填到主窗口输入框。用户审阅或编辑后点击"发送"，调用 DeepSeek `deepseek-v4-pro` 大模型，流式返回回答并以打字机效果在对话窗口展示。

## 2. 核心约束

- 必须支持窗口失焦时也能用快捷键录音（全局热键）。
- 录音、识别、对话三个阶段都不能阻塞主窗口交互。
- 配置（API Key、密钥、快捷键）通过 `config.json` 文件 + 环境变量管理，环境变量优先级更高。
- 不在第一版引入 GUI 设置面板，但保留"打开配置文件 / 重载配置"入口。

## 3. 技术栈

| 层 | 选型 | 理由 |
|----|------|------|
| 框架 | Wails v2 | Go 后端 + HTML/CSS/JS 前端，体积小（系统 WebView2），便于做现代聊天 UI |
| GUI 前端 | 原生 HTML/CSS/JS | 项目规模小，避免引入 Vue/React 工具链 |
| Markdown 渲染 | `marked` + `highlight.js` | AI 回答含代码块需要良好的 Markdown 与代码高亮 |
| 全局热键 | `golang.design/x/hotkey` | 跨平台，纯 Go 友好 |
| 录音 | `github.com/gen2brain/malgo` | miniaudio 绑定，无外部 dll 依赖，可直接配置 16kHz/单声道/int16 |
| ASR | `github.com/tencentcloud/tencentcloud-speech-sdk-go/asr` 的 `FlashRecognizer` | 一句话识别接口，匹配"按住-松开"片段式录音 |
| LLM | DeepSeek `deepseek-v4-pro`，OpenAI 兼容 `/chat/completions` | 用户指定，支持思考模式 |
| 流式协议 | SSE（`stream:true`） | 打字机效果减少等待感 |

## 4. 架构

```
┌─────────────────────────────────────────────────┐
│              Wails v2 应用窗口                    │
│  ┌────────────────────┐  ┌────────────────────┐│
│  │ 前端 (HTML/CSS/JS) │← →│  Go 后端           ││
│  │  - 聊天气泡         │   │  - 全局热键监听     ││
│  │  - Markdown 渲染    │   │  - 麦克风录音       ││
│  │  - 输入框           │   │  - 腾讯云 ASR       ││
│  │  - 流式打字效果     │   │  - DeepSeek SSE 流式││
│  │  - 录音状态指示     │   │  - 配置加载         ││
│  └────────────────────┘  └────────────────────┘│
└─────────────────────────────────────────────────┘
```

### 项目结构

```
MockAgent/
├── tencentcloud-speech-sdk-go/    # 已有的腾讯云 SDK 源码（用 replace 指向本地）
├── app/                            # 主程序
│   ├── go.mod
│   ├── main.go                     # Wails 入口
│   ├── wails.json                  # Wails 配置
│   ├── app.go                      # App 类型，绑定到前端的方法
│   ├── internal/
│   │   ├── config/config.go        # 加载 config.json + 环境变量覆盖
│   │   ├── hotkey/hotkey.go        # 全局热键监听，发送按下/松开事件
│   │   ├── recorder/recorder.go    # malgo 录音
│   │   ├── asr/asr.go              # 封装 FlashRecognizer
│   │   ├── llm/deepseek.go         # DeepSeek 流式调用
│   │   ├── conversation/conv.go    # 会话历史管理 + 导出
│   │   └── tray/tray.go            # 系统托盘
│   └── frontend/
│       ├── index.html
│       ├── src/main.js
│       ├── src/style.css
│       └── package.json
├── config.example.json
├── docs/specs/2026-05-15-mockagent-design.md
└── README.md
```

### 模块边界

| 模块 | 输入 | 输出 | 依赖 |
|------|------|------|------|
| `config` | 配置文件 + 环境变量 | `Config` 结构体 | - |
| `hotkey` | 配置中的快捷键名 | OnPress / OnRelease 回调 | golang.design/x/hotkey |
| `recorder` | 开始/停止指令 | 完整 PCM `[]byte`（停止时返回） | malgo |
| `asr` | PCM `[]byte` | 识别文本 string | 腾讯云 SDK |
| `llm` | messages 历史 | SSE 文本片段流（`chan Delta`） | net/http |
| `conversation` | 用户消息、AI 消息 | 内存中维护的 messages 列表；导出 md/json | - |
| `tray` | 显示/隐藏指令 | 系统托盘菜单事件 | `github.com/getlantern/systray`（Wails v2 自身未内置托盘 API，托盘是 v3 才有的特性） |
| `app` | 前端 RPC 调用 | 协调上述模块、Wails 事件推送 | 上述全部 |

## 5. 关键交互流程

1. **启动**：`config.Load()` → 初始化各模块 → Wails 启动主窗口 + 系统托盘
2. **按住 F2**：`hotkey.OnPress` → `recorder.Start()` → 前端事件 `recording-started`，UI 显示红点 + 计时
3. **松开 F2**：`hotkey.OnRelease` → `pcm := recorder.Stop()` → 前端事件 `recording-stopped`
4. **识别**：后端 `asr.Recognize(pcm)`（异步） → 前端事件 `asr-progress` 显示"识别中..." → 完成后事件 `asr-result`，前端把文本填入输入框
5. **发送**：用户审阅/编辑 → 点击"发送"或按 Enter → 前端调用 `app.SendMessage(text)`
6. **流式回答**：`app` 把消息加进 `conversation` → `llm.Stream(messages)` → 每个 SSE 片段通过事件 `llm-delta` 推到前端 → 完成时 `llm-done`
7. **关闭窗口**：默认最小化到托盘，主进程仍运行，全局热键依旧生效；通过托盘菜单"退出"才彻底结束

## 6. 数据契约

### 6.1 配置文件 `config.json`

```json
{
  "tencent": {
    "app_id": "1234567890",
    "secret_id": "AKID...",
    "secret_key": "..."
  },
  "deepseek": {
    "api_key": "sk-...",
    "base_url": "https://api.deepseek.com",
    "model": "deepseek-v4-pro",
    "thinking": "enabled",
    "reasoning_effort": "medium",
    "system_prompt": "You are a helpful assistant."
  },
  "hotkey": "F2",
  "audio": {
    "sample_rate": 16000,
    "channels": 1,
    "min_duration_ms": 300
  }
}
```

### 6.2 环境变量（覆盖配置文件）

- `TENCENT_APP_ID` / `TENCENT_SECRET_ID` / `TENCENT_SECRET_KEY`
- `DEEPSEEK_API_KEY` / `DEEPSEEK_MODEL` / `DEEPSEEK_BASE_URL`
- `MOCK_AGENT_HOTKEY`

### 6.3 快捷键格式

- 单键：`F1`-`F12`、`Space`
- 修饰键 + 键：`Ctrl+Alt+Space`、`Ctrl+Shift+R`、`Alt+Q`

### 6.4 消息历史

```go
type Message struct {
    Role             string `json:"role"`              // "system" / "user" / "assistant"
    Content          string `json:"content"`
    ReasoningContent string `json:"reasoning_content,omitempty"`
}
```

### 6.5 前后端事件

| 事件名 | 方向 | 数据 |
|--------|------|------|
| `recording-started` | Go→JS | - |
| `recording-stopped` | Go→JS | - |
| `asr-progress` | Go→JS | `{stage:"recognizing"}` |
| `asr-result` | Go→JS | `{text: string}` |
| `asr-error` | Go→JS | `{error: string}` |
| `llm-delta` | Go→JS | `{content?: string, reasoning?: string}` |
| `llm-done` | Go→JS | `{full_content: string}` |
| `llm-error` | Go→JS | `{error: string}` |
| `tray-show-window` | Tray→主窗口 | - |

### 6.6 前端可调用的后端方法（Wails bindings）

- `SendMessage(text string)` - 发送消息，触发 LLM 流式响应
- `StopGeneration()` - 取消正在进行的 LLM 流式响应
- `NewConversation()` - 清空当前会话历史，重新初始化 system prompt
- `GetConfig()` - 读取当前配置（用于 UI 显示快捷键等）
- `OpenConfigFile()` - 在系统编辑器中打开 `config.json`
- `ReloadConfig()` - 重新加载配置（无需重启程序）；语义：重读 `config.json` 与环境变量；如果 `hotkey` 变化则重新注册全局热键；其余字段下次调用 ASR/LLM 时自动生效；不会中断当前正在进行的录音或 LLM 流
- `ExportConversation(format string)` - `format` 为 `"md"` 或 `"json"`，弹出系统保存对话框

## 7. UI 设计

```
┌─────────────────────────────────────────────────────┐
│  MockAgent                       📥 ⚙ 🗑 [_][□][×]   │
├─────────────────────────────────────────────────────┤
│  💬 你: 帮我写一个二分查找                            │
│                                                      │
│  🤖 AI:                                              │
│  ▸ 思考中...（折叠的思考过程）                         │
│  ```python                                           │
│  def binary_search(arr, target):                     │
│      ...                                             │
│  ```                                                 │
│                                                      │
├─────────────────────────────────────────────────────┤
│ [● 录音中 0.8s / 🎤 按 F2 录音]   ⏸ 停止生成         │
├─────────────────────────────────────────────────────┤
│ ┌──────────────────────────────────┐ ┌──┐ ┌─────┐   │
│ │ 输入消息或按 F2 录音...           │ │🎤│ │发送 │   │
│ └──────────────────────────────────┘ └──┘ └─────┘   │
└─────────────────────────────────────────────────────┘
```

- 默认窗口大小 900×700，最小 600×500
- 用户消息右对齐（蓝色底），AI 消息左对齐（灰白底），AI 消息支持 Markdown + 代码块复制按钮
- "思考过程"默认折叠，显示"已思考 N 秒"，可点开
- 录音状态栏：空闲 / 录音中（红点闪烁 + 计时）/ 识别中 / 错误
- 输入框 `Enter` 发送，`Shift+Enter` 换行，自动多行扩展（最多 6 行后内部滚动）
- 输入框右侧的 🎤 按钮：作为全局热键的后备入口，按住开始录音，松开结束录音（鼠标 mousedown / mouseup 触发与 F2 完全相同的流程）
- AI 流式输出时"发送"按钮变为"停止生成"
- 标题栏按钮：📥 导出（Markdown / JSON）、⚙ 打开配置文件、🗑 新建对话
- 默认深色主题

## 8. 系统托盘

- 关闭按钮 [×] **固定**为最小化到系统托盘
- 托盘菜单：
  - `显示主窗口`
  - `新建对话`
  - `打开配置文件`
  - `退出`（唯一彻底退出入口）
- 双击托盘图标 = 显示主窗口

**集成约束**：`getlantern/systray` 要求在主线程运行其 `Run()` 函数（会阻塞）。Wails 的 `wails.Run()` 同样会阻塞。常见做法：将 systray 跑在独立 goroutine（Windows / Linux 上可行），托盘菜单回调通过线程安全方式调用 Wails runtime（`runtime.WindowShow` 等）。具体方案在实现时验证；若该库在 Windows 下无法以 goroutine 方式启动，则改用 `energye/systray`（getlantern 的 fork，已修复部分线程问题）作为后备。

## 9. 导出对话

- 标题栏 📥 按钮 → 弹出小菜单选择格式（Markdown / JSON）→ 系统保存对话框
- 默认文件名：`MockAgent-对话-YYYY-MM-DD-HHMM.{md|json}`
- Markdown 格式：每条消息以 `## 你` / `## AI` 标题分段，AI 的代码块原样保留
- JSON 格式：完整 messages 数组（含 reasoning_content），结构同 6.4

## 10. 错误处理与边界情况

### 录音
- 无可用麦克风：启动时检测，前端横幅提示"未找到可用音频输入设备"
- 按住时长 < 300ms：忽略，不调 ASR，前端微提示"录音过短"
- 重复按下：忽略后续按下，仅以最后一次松开为停止时机
- 设备被独占：前端事件 `asr-error: "麦克风不可用"`

### ASR
- 网络/鉴权/配额错误：前端事件 `asr-error`，UI 在状态栏 3 秒红字
- 识别返回空文本：前端提示"未识别到内容"，不填入输入框
- 识别中再次按下 F2：丢弃当前结果，开始新一轮录音

### LLM
- API Key 错误 / 余额不足：4xx 错误体解析后前端事件 `llm-error`，UI 在该消息位置显示红字
- 流式中断：累计已收内容标记为部分完成，气泡末尾追加"（连接中断）"
- 用户点"停止生成"：取消 context → 关闭 SSE → 已收内容入历史

### 配置
- `config.json` 不存在：首次启动从 `config.example.json` 复制一份；启动后 UI 横幅提示"未配置"，发送时拒绝
- JSON 解析失败：启动后显示错误页 + "打开配置文件"按钮
- 必填项空：发送/录音前检测，给出明确提示
- 热键注册失败：状态栏告警"快捷键 F2 注册失败"，仍提供 UI 内录音按钮作为后备

### 并发
- recorder 内部加锁，保证同一时刻只有一个录音流
- LLM 流式响应通过 context 控制取消

## 11. 正确性属性

*属性是系统所有有效执行下都应成立的特征或行为——一种关于系统"应该做什么"的形式化陈述，是人类可读规范与机器可验证正确性保证之间的桥梁。*

下列属性来自需求验收准则的可测试性分析（见 `2026-05-15-mockagent-requirements.md` 与 prework 笔记）。每条属性均以"对所有 …"开头，并标注其验证的需求条目。属性测试至少运行 100 次随机迭代。

### 属性 1：首次启动从示例配置拷贝

**对任何** 仅包含 `config.example.json` 而不含 `config.json` 的目录与任意有效示例 JSON 内容 *e*，调用 `Config_Loader.Load(dir)` 之后，`config.json` 文件应存在且其字节序列等于 *e*。

**Validates: Requirement 1.1**

### 属性 2：文件值与环境变量合并的覆盖语义

**对任何** 文件 Config *f* 与任意环境变量子集 *E*（仅含 `TENCENT_APP_ID`、`TENCENT_SECRET_ID`、`TENCENT_SECRET_KEY`、`DEEPSEEK_API_KEY`、`DEEPSEEK_MODEL`、`DEEPSEEK_BASE_URL`、`MOCK_AGENT_HOTKEY`），令 *m = merge(f, E)*。则对每个被 *E* 设置的字段 *k*，*m[k]* 等于 *E[k]*；对未在 *E* 中设置的字段 *k*，*m[k]* 等于 *f[k]*。

**Validates: Requirements 1.3, 1.4**

### 属性 3：热键变更时重新注册

**对任何** 旧 Hotkey_Spec *h₀* 与新 Hotkey_Spec *h₁*，在 `ReloadConfig()` 之后：当 *h₁ ≠ h₀* 时，Hotkey_Manager 应恰好执行一次 `unregister(h₀)` 与一次 `register(h₁)`；当 *h₁ = h₀* 时，不应有 `unregister` 或 `register` 调用。

**Validates: Requirement 1.5**

### 属性 4：重载不中断进行中的录音与流

**对任何** 在 Recording_Session 或 LLM_Stream 进行中执行的 `ReloadConfig()` 调用，调用前后该 Session/Stream 的活跃状态与已累积内容应保持不变。

**Validates: Requirement 1.6**

### 属性 5：敏感字段不泄露

**对任何** 包含非平凡密钥值（`secret_id`、`secret_key`、`api_key`）的 Config 实例 *c*，对 `c.String()`、`GetConfig()` 返回结构的字符串化、Config_Loader/ASR_Client/LLM_Client 任一错误返回值与日志输出做子串搜索，结果中均不应包含 *c.secret_id*、*c.secret_key*、*c.api_key* 的明文值。

**Validates: Requirements 1.8, 10.4**

### 属性 6：热键事件去重器

**对任何** Hotkey_Spec 的按下/松开事件序列 *S*，去重器输出的"开始录音"事件应正好与 *S* 中第一次按下对齐，"停止录音"事件应正好与 *S* 中最后一次松开对齐；其间所有重复按下/松开都不产生新的开始/停止。

**Validates: Requirement 2.4**

### 属性 7：Hotkey_Spec 解析与可逆性

**对任何** 由合法生成器产出的 Hotkey_Spec 字符串 *s*，`parse(s)` 应成功且 `format(parse(s))` 应与 *s* 在大小写归一化后相等；**对任何** 由非法生成器产出的字符串 *s'*（包含未知键名、空字符串、单独修饰键等），`parse(s')` 应返回错误。

**Validates: Requirements 2.6, 2.7**

### 属性 8：录音缓冲不丢字节

**对任何** 通过伪音频回调注入到 Recorder 的字节序列 *b*（按 16kHz、单声道、16-bit 写入），`Recorder.Stop()` 返回的 PCM_Buffer 应等于 *b*。

**Validates: Requirement 3.4**

### 属性 9：单录音流不变量

**对任何** 由 `Start` 与 `Stop` 操作组成的事件序列 *O*，在每个时刻 Recorder 的活跃 Recording_Session 数量都应 ≤ 1，且对处于 Recording 状态时收到的 `Start` 调用，状态保持不变且调用返回失败。

**Validates: Requirement 3.8**

### 属性 10：录音时长阈值判定

**对任何** 字节长度 *n* 与配置 `(sample_rate, channels, bytes_per_sample, min_duration_ms)`，`shouldRecognize(n, …)` 的返回值应等于 `(n / (sample_rate * channels * bytes_per_sample)) * 1000 ≥ min_duration_ms`。

**Validates: Requirement 3.9**

### 属性 11：ASR 空白结果不回填

**对任何** ASR 文本结果 *t*，`dispatchASRResult(t)` 是否把 *t* 写入输入框，应等价于 *t* 含有至少一个非空白字符。

**Validates: Requirement 4.6**

### 属性 12：新一轮按下取消上一轮 ASR

**对任何** 由 Press/Release/ASRComplete 三类事件构成的有限序列，输入框被回填的文本所属 Recording_Session *s* 应满足：在 *s* 的 ASRComplete 之前未出现新的 Press；若出现新 Press，则 *s* 的结果不应回填到输入框。

**Validates: Requirement 4.8**

### 属性 13：LLM 请求构造的不变量

**对任何** Config *c* 与 messages 数组 *M*，LLM_Client 发出的 HTTP 请求 *r* 应满足：`r.URL == c.deepseek.base_url + "/chat/completions"`；`r.Header["Authorization"] == "Bearer " + c.deepseek.api_key`；`r.Body.model == c.deepseek.model`；`r.Body.stream == true`；`r.Body.messages` 在 JSON 反序列化后等于 *M*。

**Validates: Requirements 5.2, 5.3**

### 属性 14：SSE 解析的连接律与累积

**对任何** OpenAI 风格 chunk 数组 *C* 序列化为 SSE 流并以 `[DONE]` 结尾，LLM_Client 解析后通过 `llm-delta` 推送的 `content` 增量按顺序拼接结果，应等于 *C* 中所有 `choices[0].delta.content` 拼接；`reasoning` 增量同理。`llm-done` 事件的 `full_content` 与 Conversation_Store 中最后一条 assistant Message 的 `content` 都应等于该拼接结果。

**Validates: Requirements 5.5, 5.6, 5.7**

### 属性 15：4xx 错误不污染会话

**对任何** 4xx 状态响应 *r* 与请求前会话 *M*，调用 `SendMessage` 之后 Conversation_Store 的状态应等于"在 *M* 末尾追加用户消息"，即不应追加 assistant Message；同时应有一次 `llm-error` 事件被发送。

**Validates: Requirement 5.9**

### 属性 16：单 LLM 流不变量

**对任何** 由 `SendMessage`、`StreamComplete`、`StopGeneration` 操作组成的事件序列，在每个时刻活跃的 LLM_Stream 数量都应 ≤ 1，且当存在活跃流时收到的 `SendMessage` 调用应返回错误且不创建新流。

**Validates: Requirement 5.11**

### 属性 17：NewConversation 重置

**对任何** System_Prompt *p* 与任意已存在的 messages 列表 *M*，`NewConversation()` 之后 Conversation_Store 的 messages 应等于：当 *p* 为空时为空列表；当 *p* 非空时为 `[{role:"system", content:p}]`。

**Validates: Requirement 6.1**

### 属性 18：会话顺序保留

**对任何** 由 (role, content) 二元组组成的追加操作序列 *A*，Conversation_Store 在序列结束后的 messages 顺序应与 *A* 中追加顺序逐项一致。

**Validates: Requirement 6.2**

### 属性 19：导出默认文件名格式

**对任何** 时间 *t* 与格式 *f ∈ {"md", "json"}*，`generateExportFilename(t, f)` 的返回字符串应匹配正则 `^MockAgent-对话-\d{4}-\d{2}-\d{2}-\d{4}\.(md|json)$` 且后缀等于 *f*。

**Validates: Requirements 7.1, 7.2**

### 属性 20：Markdown 导出结构保真

**对任何** messages 列表 *M*，导出的 Markdown 字符串 *s* 应满足：*s* 中 `## 你` 出现次数等于 *M* 中 user Message 数；`## AI` 出现次数等于 assistant Message 数；对每条 assistant Message 中的三反引号代码块，*s* 应原样包含其原始字符串。

**Validates: Requirement 7.4**

### 属性 21：JSON 导出往返一致

**对任何** messages 列表 *M*，对 *M* 调用 `ExportConversation("json", path)` 后再用 `json.Unmarshal` 读回得到的列表 *M'* 应满足 *M' = M*（顺序与所有字段，含 `reasoning_content`，逐项相等）。

**Validates: Requirements 7.5, 7.7**

### 属性 22：非法导出格式被拒绝

**对任何** 不属于 `{"md", "json"}` 的字符串 *f*，`ExportConversation(f, path)` 应返回错误且不创建文件、不弹出保存对话框。

**Validates: Requirement 7.6**

### 属性 23：代码块复制保真

**对任何** assistant Message 中的代码块字符串 *k*（含任意缩进、空白与换行），渲染并点击该代码块的"复制"按钮后，系统剪贴板内容应严格等于 *k*。

**Validates: Requirement 8.3**

### 属性 24：输入框高度上限

**对任何** 输入框中行数 *n*，输入框可视高度应等于 `min(n, 6) * lineHeight`，且当 *n > 6* 时输入框内部出现纵向滚动条。

**Validates: Requirement 8.8**

### 属性 25：流式渲染时滚动贴底保持

**对任何** 滚动状态 *s*（贴底/非贴底）与 `llm-delta` 事件 *d*，事件处理后：若 *s* 为贴底则处理后仍为贴底；若 *s* 为非贴底则不应被强制贴底。

**Validates: Requirement 8.5**

### 属性 26：关闭主窗口后热键仍就绪

**对任何** "关闭主窗口 → 按下 Hotkey_Spec → 松开 Hotkey_Spec" 事件序列，OnPress 与 OnRelease 回调仍应分别被触发恰好一次，且主进程未退出。

**Validates: Requirement 9.3**

### 属性 27：缺凭证时拒绝外部调用

**对任何** 用户输入 *t* 与缺失任一密钥字段的 Config *c*：当 `c.deepseek.api_key == ""` 时 `SendMessage(t)` 应返回错误且 LLM_Client 不发起 HTTP 请求；当 `c.tencent.app_id`、`c.tencent.secret_id`、`c.tencent.secret_key` 任一为空时，OnPress 路径不应调用 ASR_Client。

**Validates: Requirements 10.1, 10.2**

## 12. 第一版范围（YAGNI）

**包含：**
- 全局热键 F2 录音 → ASR → 输入框 → 发送 → DeepSeek 流式回答
- 单会话连续对话；新建对话清空
- 导出对话（Markdown / JSON）
- 关闭按钮最小化到托盘
- 配置文件 + 环境变量；UI 提供"打开配置 / 重载配置"

**不包含：**
- 多会话历史 / 侧边栏 / 会话切换
- 主题切换
- 代码块行号
- 设置面板 GUI（修改通过编辑 `config.json` + ReloadConfig）
- 多会话导入恢复（JSON 导出格式预留兼容，但本版不实现导入）

## 13. 未来可扩展点

- 升级到实时流式 ASR（SpeechRecognizer）做"边说边出字"
- 多会话历史持久化与切换
- 设置面板 GUI
- 系统级 PTT 指示灯（按住时屏幕边缘 LED 效果）
