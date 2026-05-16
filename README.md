# MockAgent

一个本地运行的桌面语音对话助手：按住快捷键说话 → 腾讯云语音识别转文字 → 发给 DeepSeek 大模型流式回答。

- 后端：Go + Wails v2
- 录音：`malgo`（16 kHz 单声道 PCM）
- 语音识别：腾讯云 [tencentcloud-speech-sdk-go](./tencentcloud-speech-sdk-go)（FlashRecognizer）
- 大模型：DeepSeek `deepseek-v4-pro`（OpenAI 兼容 SSE 流式）
- 全局热键：`golang.design/x/hotkey`
- 系统托盘：`getlantern/systray`

## 功能

- 全局快捷键（默认 **F2**）按住录音、松开识别，窗口失焦也能用
- 识别结果回填到输入框，用户审阅 / 编辑后再点击发送
- AI 回答以打字机效果流式渲染，支持 Markdown 与代码高亮（含一键复制）
- 思考过程默认折叠，显示"已思考 N 秒"，可点开查看
- 关闭主窗口最小化到系统托盘（热键继续工作），托盘菜单可"显示主窗口 / 新建对话 / 打开配置 / 退出"
- 导出对话为 Markdown 或 JSON
- 输入框右侧有 🎤 备用按钮，按住时与全局热键等价

## 配置

第一次启动时如果没有 `config.json`，程序会从 `config.example.json` 复制一份。请编辑里面的密钥：

```json
{
  "tencent": {
    "app_id": "你的腾讯云 AppID",
    "secret_id": "你的 SecretID",
    "secret_key": "你的 SecretKey"
  },
  "deepseek": {
    "api_key": "sk-你的 DeepSeek Key",
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

也可以用环境变量覆盖（优先级高于文件）：

| 字段 | 环境变量 |
|------|----------|
| `tencent.app_id` | `TENCENT_APP_ID` |
| `tencent.secret_id` | `TENCENT_SECRET_ID` |
| `tencent.secret_key` | `TENCENT_SECRET_KEY` |
| `deepseek.api_key` | `DEEPSEEK_API_KEY` |
| `deepseek.model` | `DEEPSEEK_MODEL` |
| `deepseek.base_url` | `DEEPSEEK_BASE_URL` |
| `hotkey` | `MOCK_AGENT_HOTKEY` |

### 快捷键格式

- 单键：`F1`–`F12`、`Space`
- 组合键：`Ctrl+Alt+Space`、`Ctrl+Shift+R`、`Alt+Q` 等
- 修饰键支持 `Ctrl` / `Control` / `Alt` / `Shift` / `Win` / `Super` / `Meta`，大小写不敏感

## 开发环境

- Go 1.21+（项目使用 1.23）
- Node.js 18+
- Windows 上需要一份较新的 GCC，例如 [winlibs MinGW-w64 GCC 14](https://github.com/brechtsanders/winlibs_mingw/releases) 解压到 `C:\tools\mingw64`，把 `bin/` 加到 PATH

```pwsh
# 安装 Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# 验证环境
wails doctor
```

## 构建与运行

```pwsh
cd app

# 跑测试（仅后端单元测试，运行很快）
go test ./...

# 开发模式（热重载前端）
wails dev

# 生产构建
wails build
# 产物在 app\build\bin\app.exe
```

`config.json` 的查找顺序：可执行文件同目录 → 当前工作目录 → 上一级目录。开发模式 (`wails dev`) 把 `config.json` 放在仓库根目录最方便。

## 项目结构

```
MockAgent/
├── tencentcloud-speech-sdk-go/    # 腾讯云语音 SDK 源码（go.mod replace 指向本地）
├── app/
│   ├── main.go / app.go           # Wails 入口与协调器
│   ├── internal/
│   │   ├── config/                # 配置加载、环境变量覆盖、密钥掩码
│   │   ├── hotkey/                # 快捷键解析、按下/松开去重、注册
│   │   ├── recorder/              # malgo 录音（实现 + Fake 测试用）
│   │   ├── asr/                   # 腾讯云 FlashRecognizer 封装
│   │   ├── llm/                   # DeepSeek SSE 流式调用
│   │   ├── conversation/          # 会话历史 + Markdown/JSON 导出
│   │   └── tray/                  # 系统托盘（独立 goroutine）
│   ├── frontend/
│   │   ├── index.html / src/      # 原生 HTML / CSS / JS（深色主题）
│   │   └── wailsjs/               # Wails 自动生成的前后端绑定
│   └── build/                     # 图标、构建产物
├── docs/specs/                    # 设计文档与任务列表
├── config.example.json            # 配置模板
└── README.md
```

## 文档

- [设计文档](docs/specs/2026-05-15-mockagent-design.md)
- [需求文档](docs/specs/2026-05-15-mockagent-requirements.md)
- [任务列表](docs/specs/2026-05-15-mockagent-tasks.md)

## 许可

腾讯云 SDK 部分版权属于腾讯，许可见 [tencentcloud-speech-sdk-go/LICENSE](tencentcloud-speech-sdk-go/LICENSE)。
