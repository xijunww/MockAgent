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

## 11. 第一版范围（YAGNI）

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

## 12. 未来可扩展点

- 升级到实时流式 ASR（SpeechRecognizer）做"边说边出字"
- 多会话历史持久化与切换
- 设置面板 GUI
- 系统级 PTT 指示灯（按住时屏幕边缘 LED 效果）
