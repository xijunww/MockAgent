# MockAgent

MockAgent 是一个本地运行的桌面语音对话助手：按住快捷键录音，将麦克风或系统声音转写为文字，审阅后发送给 DeepSeek，并流式显示回答。

- 后端：Go + Wails v2
- 录音：`malgo`（16 kHz 单声道 PCM）
- 语音识别：腾讯云 `github.com/tencentcloud/tencentcloud-speech-sdk-go`（FlashRecognizer）
- 大模型：DeepSeek `deepseek-v4-pro`（OpenAI 兼容 SSE 流式）
- 全局热键：`golang.design/x/hotkey`
- 系统托盘：`getlantern/systray`

## 功能

- 默认按住 **F2** 录麦克风，松开后调用腾讯云 ASR，并把结果回填到输入框
- 默认按住 **F3** 录系统声音，适合会议、面试等场景里的对方语音
- 默认按 **F4** 直接发送当前输入框内容；输入为空或正在生成时会被拒绝
- 输入框右侧的麦克风按钮与 F2 录音等价
- AI 回答流式渲染，支持 Markdown、代码高亮和代码块一键复制
- 支持 DeepSeek thinking 内容折叠展示（启用 `thinking` 时）
- 支持编辑系统提示词，并保留提示词历史
- 支持添加参考文档，启用的文档会拼接到 system prompt 后发送给模型
- 支持导出当前对话为 Markdown 或 JSON
- 系统托盘提供显示主窗口、新建对话、打开配置和退出入口

## 配置

第一次启动时如果没有 `config.json`，程序会从 `config.example.json` 复制一份。请编辑复制出的 `config.json` 并填入密钥。当前推荐配置结构如下：

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
    "system_prompt_history": [
      "You are a helpful assistant."
    ],
    "active_system_prompt": "You are a helpful assistant."
  },
  "record_hotkey": "F2",
  "send_hotkey": "F4",
  "system_hotkey": "F3",
  "audio": {
    "sample_rate": 16000,
    "channels": 1,
    "min_duration_ms": 300
  }
}
```

旧版 `hotkey` 和 `deepseek.system_prompt` 字段仍可被读取并迁移；新配置请使用 `record_hotkey`、`send_hotkey`、`system_hotkey`、`system_prompt_history` 和 `active_system_prompt`。

也可以用环境变量覆盖部分字段（优先级高于文件）：

| 字段 | 环境变量 |
|------|----------|
| `tencent.app_id` | `TENCENT_APP_ID` |
| `tencent.secret_id` | `TENCENT_SECRET_ID` |
| `tencent.secret_key` | `TENCENT_SECRET_KEY` |
| `deepseek.api_key` | `DEEPSEEK_API_KEY` |
| `deepseek.model` | `DEEPSEEK_MODEL` |
| `deepseek.base_url` | `DEEPSEEK_BASE_URL` |
| `record_hotkey` | `MOCK_AGENT_RECORD_HOTKEY` |
| `send_hotkey` | `MOCK_AGENT_SEND_HOTKEY` |
| `system_hotkey` | `MOCK_AGENT_SYSTEM_HOTKEY` |

`MOCK_AGENT_HOTKEY` 仍作为旧环境变量兼容，只会覆盖录音热键。

### 快捷键格式

- 单键：`F1`-`F12`、`Space`
- 组合键：`Ctrl+Alt+Space`、`Ctrl+Shift+R`、`Alt+Q` 等
- 修饰键支持 `Ctrl` / `Control` / `Alt` / `Shift` / `Win` / `Super` / `Meta`，大小写不敏感

## 开发环境

- Go 1.24.1（与 `app/go.mod` 保持一致）
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

# 首次检出或清理后，先生成 frontend/dist 供 Go embed 使用
cd frontend
npm install
npm run build
cd ..

# 后端单元测试
go test ./...

# 开发模式
wails dev

# 生产构建
wails build
# 产物在 app\build\bin\app.exe
```

`config.json` 的查找顺序：可执行文件同目录 -> 当前工作目录 -> 上一级目录。开发模式 (`wails dev`) 把 `config.json` 放在仓库根目录最方便。

## 项目结构

```
MockAgent/
├── app/
│   ├── main.go / app.go           # Wails 入口与后端协调器
│   ├── internal/
│   │   ├── config/                # 配置加载、环境变量覆盖、密钥掩码
│   │   ├── hotkey/                # 快捷键解析、按下/松开去重、注册
│   │   ├── recorder/              # 麦克风与系统声音录制
│   │   ├── asr/                   # 腾讯云 FlashRecognizer 封装
│   │   ├── llm/                   # DeepSeek SSE 流式调用
│   │   ├── conversation/          # 会话历史与导出
│   │   ├── docs/                  # 参考文档提取、索引和拼接
│   │   └── tray/                  # 系统托盘
│   ├── frontend/
│   │   ├── index.html / src/      # 原生 HTML / CSS / JS
│   │   └── wailsjs/               # Wails 自动生成，未提交
│   ├── build/                     # 图标与平台构建配置
│   └── go.mod                     # app 子模块依赖
├── docs/specs/                    # 设计文档、需求文档与任务列表
├── config.example.json            # 配置模板
└── README.md
```

腾讯云语音 SDK 现在通过官方 Go module 引入，不再在仓库中提交 `tencentcloud-speech-sdk-go/` 源码目录。

## 文档

- [设计文档](docs/specs/2026-05-15-mockagent-design.md)
- [需求文档](docs/specs/2026-05-15-mockagent-requirements.md)
- [任务列表](docs/specs/2026-05-15-mockagent-tasks.md)
