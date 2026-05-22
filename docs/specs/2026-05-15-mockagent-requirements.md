# MockAgent 需求文档

**日期**：2026-05-15
**关联设计**：`2026-05-15-mockagent-design.md`
**状态**：基于已批准设计派生
**维护备注**：2026-05-22 已同步当前实现中的三类热键、系统提示词新字段，以及腾讯云 SDK 远程 module 依赖。

## 简介

MockAgent 是一款基于 Wails v2 的 Windows/跨平台桌面助手。用户在任意前台应用中按住全局热键 F2 录音，松开后由腾讯云 ASR（FlashRecognizer）将音频转写为文字并回填到主窗口输入框；用户审阅后发送给 DeepSeek `deepseek-v4-pro` 模型，模型以 SSE 流式返回，前端以打字机效果渲染 Markdown 与代码块。本需求文档把第 11 节"第一版范围"中的功能逐一形式化为 EARS 验收准则。

## 术语表

- **MockAgent_App**：本桌面应用整体（Wails 主进程 + 前端 + 后端模块）。
- **Config_Loader**：负责读取 `config.json` 与环境变量并构造 `Config` 结构体的模块（`internal/config`）。
- **Hotkey_Manager**：基于 `golang.design/x/hotkey` 注册并监听全局热键、向上层派发按下/松开事件的模块（`internal/hotkey`）。
- **Recorder**：基于 `malgo` 的麦克风采集模块（`internal/recorder`）。
- **ASR_Client**：基于 `tencentcloud-speech-sdk-go` `FlashRecognizer` 的语音识别模块（`internal/asr`）。
- **LLM_Client**：调用 DeepSeek OpenAI 兼容 `/chat/completions` 接口、解析 SSE 的模块（`internal/llm`）。
- **Conversation_Store**：内存中维护 `messages` 列表并支持导出的模块（`internal/conversation`）。
- **Tray_Manager**：基于 `getlantern/systray`（或 `energye/systray` 后备）的系统托盘模块（`internal/tray`）。
- **App_Coordinator**：在 `app.go` 中协调上述模块、暴露 Wails 绑定方法并发出 Wails 运行时事件的协调层。
- **Frontend_UI**：`frontend/` 下的原生 HTML/CSS/JS 前端，包括聊天列表、输入框、录音状态栏、标题栏按钮等。
- **Config**：见设计 6.1 的 `config.json` 结构与 6.2 的环境变量集合，二者合成的最终配置对象，环境变量覆盖文件值。
- **Hotkey_Spec**：用户配置的快捷键字符串（设计 6.3 的格式），如 `F2`、`Ctrl+Alt+Space`。
- **Recording_Session**：从某次"按下 Hotkey_Spec"到对应"松开 Hotkey_Spec"之间的一段录音过程。
- **PCM_Buffer**：单次 `Recording_Session` 累积的 16kHz、单声道、16 位带符号 PCM 数据 `[]byte`。
- **Min_Duration_Ms**：`config.audio.min_duration_ms`（默认 300），低于该时长的录音视为无效。
- **Message**：见设计 6.4 的 `Message` 结构体（`role`、`content`、可选 `reasoning_content`）。
- **System_Prompt**：`config.deepseek.active_system_prompt`，由 `system_prompt_history` 中的当前活跃项决定，并在发送给 LLM 前即时拼接为 `role=system` 消息。
- **LLM_Stream**：`LLM_Client` 一次流式调用产生的 delta 序列。
- **Hidden_Field**：在序列化或日志输出中**不得**出现明文值的字段，包括 `tencent.secret_id`、`tencent.secret_key`、`deepseek.api_key`。

## 需求

### 需求 1：配置加载与重载

**用户故事**：作为用户，我希望通过 `config.json` 文件和环境变量管理 API 密钥与快捷键，并能在不重启程序的情况下重载配置，以便快速调整设置。

#### 验收准则

1. WHEN MockAgent_App 启动且 `config.json` 不存在，THE Config_Loader SHALL 从同目录的 `config.example.json` 复制一份 `config.json` 并继续启动流程。
2. WHEN MockAgent_App 启动且 `config.json` 存在但 JSON 解析失败，THE App_Coordinator SHALL 在 Frontend_UI 显示错误页并提供"打开配置文件"按钮，且不进入正常聊天界面。
3. WHEN Config_Loader 加载配置，THE Config_Loader SHALL 先读取 `config.json` 的字段值，再用环境变量 `TENCENT_APP_ID`、`TENCENT_SECRET_ID`、`TENCENT_SECRET_KEY`、`DEEPSEEK_API_KEY`、`DEEPSEEK_MODEL`、`DEEPSEEK_BASE_URL`、`MOCK_AGENT_RECORD_HOTKEY`、`MOCK_AGENT_SEND_HOTKEY`、`MOCK_AGENT_SYSTEM_HOTKEY` 中已设置的项覆盖对应字段；旧环境变量 `MOCK_AGENT_HOTKEY` 仅兼容覆盖录音热键。
4. WHEN 前端调用 `ReloadConfig()`，THE App_Coordinator SHALL 重新执行 Config_Loader 的加载流程，得到新的 Config 对象。
5. WHEN `ReloadConfig()` 后任一热键（`record_hotkey` / `send_hotkey` / `system_hotkey`）与旧 Hotkey_Spec 不同，THE Hotkey_Manager SHALL 注销旧热键并注册新 Hotkey_Spec。
6. WHEN `ReloadConfig()` 在录音或 LLM_Stream 进行中被调用，THE App_Coordinator SHALL 不中断当前 Recording_Session 与 LLM_Stream，且新的 ASR/LLM 配置仅在下次调用时生效。
7. WHEN 前端调用 `OpenConfigFile()`，THE App_Coordinator SHALL 通过系统默认编辑器打开 `config.json` 文件。
8. THE Config_Loader SHALL 在任何错误信息、日志输出与 `GetConfig()` 返回值中以掩码形式（例如固定字符串 `***`）表示 Hidden_Field 的值。

### 需求 2：全局热键

**用户故事**：作为用户，我希望即使 MockAgent 窗口不在前台也能通过 F2 录音，以便在任何应用中快速使用。

#### 验收准则

1. WHEN MockAgent_App 启动且 Config 中的 Hotkey_Spec 解析成功，THE Hotkey_Manager SHALL 注册该快捷键为系统级全局热键。
2. WHEN 用户按下 Hotkey_Spec，THE Hotkey_Manager SHALL 触发一次 `OnPress` 回调，无论当前前台窗口是否为 MockAgent_App。
3. WHEN 用户松开 Hotkey_Spec，THE Hotkey_Manager SHALL 触发一次 `OnRelease` 回调。
4. WHILE 一次按下尚未松开时再次收到同一 Hotkey_Spec 的按下事件，THE Hotkey_Manager SHALL 忽略后续按下并以最后一次松开作为唯一停止时机。
5. IF Hotkey_Manager 注册全局热键失败，THEN THE App_Coordinator SHALL 在 Frontend_UI 状态栏显示"快捷键 X 注册失败"（X 为 Hotkey_Spec），并继续保留 UI 内麦克风按钮作为后备录音入口。
6. THE Hotkey_Manager SHALL 接受设计 6.3 中"单键"与"修饰键 + 键"两类格式（`F1`–`F12`、`Space`、`Ctrl+`、`Alt+`、`Shift+` 的组合）。
7. IF Hotkey_Spec 不符合设计 6.3 的格式规范，THEN THE Hotkey_Manager SHALL 拒绝注册并向 App_Coordinator 返回带有失败原因的错误。

### 需求 3：麦克风录音

**用户故事**：作为用户，我希望按住热键时麦克风开始采集音频、松开时停止并得到完整 PCM，以便交给 ASR 转写。

#### 验收准则

1. WHEN MockAgent_App 启动，THE Recorder SHALL 探测系统是否存在可用音频输入设备。
2. IF 启动时未发现可用音频输入设备，THEN THE App_Coordinator SHALL 在 Frontend_UI 顶部显示横幅"未找到可用音频输入设备"。
3. WHEN App_Coordinator 在 `OnPress` 中调用 `Recorder.Start()`，THE Recorder SHALL 以 16000 Hz 采样率、单声道、16 位带符号整型格式从默认输入设备开始采集 PCM 数据。
4. WHEN App_Coordinator 在 `OnRelease` 中调用 `Recorder.Stop()`，THE Recorder SHALL 停止采集，关闭设备，并返回本次 Recording_Session 累积的完整 PCM_Buffer。
5. WHEN Recorder 完成 `Start()` 调用，THE App_Coordinator SHALL 向 Frontend_UI 发送 `recording-started` 事件。
6. WHEN Recorder 完成 `Stop()` 调用，THE App_Coordinator SHALL 向 Frontend_UI 发送 `recording-stopped` 事件。
7. IF `Recorder.Start()` 因设备被独占或其他系统错误失败，THEN THE App_Coordinator SHALL 向 Frontend_UI 发送 `asr-error` 事件，错误消息为"麦克风不可用"或更具体的原因，且不进入 ASR 流程。
8. WHILE 一个 Recording_Session 进行中时再次调用 `Recorder.Start()`，THE Recorder SHALL 拒绝该调用并保持当前 Recording_Session 不受影响。
9. IF 一次 Recording_Session 的录音时长小于 Min_Duration_Ms，THEN THE App_Coordinator SHALL 不调用 ASR_Client，且向 Frontend_UI 发送提示性事件使其显示"录音过短"。

### 需求 4：语音识别

**用户故事**：作为用户，我希望松开热键后程序自动把录音转写为中文文字并回填到输入框，以便审阅或编辑。

#### 验收准则

1. WHEN App_Coordinator 取得有效的 PCM_Buffer，THE App_Coordinator SHALL 在不阻塞 Frontend_UI 输入与聊天列表渲染的前提下调用 `ASR_Client.Recognize(pcm)`。
2. WHEN ASR_Client 开始识别，THE App_Coordinator SHALL 向 Frontend_UI 发送 `asr-progress` 事件，载荷为 `{stage:"recognizing"}`。
3. WHEN ASR_Client 通过 FlashRecognizer 接口请求腾讯云语音识别服务，THE ASR_Client SHALL 使用 Config 中 `tencent.app_id`、`tencent.secret_id`、`tencent.secret_key` 三项凭证。
4. WHEN ASR_Client 成功返回非空文本，THE App_Coordinator SHALL 向 Frontend_UI 发送 `asr-result` 事件，载荷为 `{text: 识别文本}`。
5. WHEN Frontend_UI 收到 `asr-result` 事件，THE Frontend_UI SHALL 把 `text` 写入输入框，且不自动发送给 LLM。
6. IF ASR_Client 返回空字符串或仅含空白字符的结果，THEN THE App_Coordinator SHALL 向 Frontend_UI 发送提示性事件使其显示"未识别到内容"，且不写入输入框。
7. IF ASR_Client 因网络、鉴权或配额错误失败，THEN THE App_Coordinator SHALL 向 Frontend_UI 发送 `asr-error` 事件，载荷为 `{error: 错误描述}`，并由 Frontend_UI 在状态栏以红字显示 3 秒。
8. WHILE 一次 ASR 识别进行中时收到新的 `OnPress` 事件，THE App_Coordinator SHALL 取消等待该次 ASR 结果（即使后续返回也不再回填输入框），并启动新的 Recording_Session。

### 需求 5：DeepSeek 流式对话

**用户故事**：作为用户，我希望审阅完文字后点击发送，能立即看到 AI 以打字机效果流式输出回答与思考过程，以便快速获得反馈。

#### 验收准则

1. WHEN Frontend_UI 调用 `SendMessage(text)` 且 `text` 非空白，THE App_Coordinator SHALL 向 Conversation_Store 追加一条 `role="user"`、`content=text` 的 Message。
2. WHEN App_Coordinator 完成用户消息追加，THE LLM_Client SHALL 以 Conversation_Store 当前完整 messages 列表向 `${deepseek.base_url}/chat/completions` 发起 HTTP POST 请求。
3. THE LLM_Client SHALL 在请求体中设置 `model=config.deepseek.model`、`stream=true`，并使用 `Authorization: Bearer ${config.deepseek.api_key}` 头。
4. WHERE Config 中 `deepseek.thinking` 为 `"enabled"`，THE LLM_Client SHALL 在请求体中包含 `thinking` 与 `reasoning_effort` 字段以启用思考模式。
5. WHEN LLM_Client 成功建立 SSE 连接并收到一段包含 `content` 增量的 chunk，THE App_Coordinator SHALL 向 Frontend_UI 发送 `llm-delta` 事件，载荷至少包含 `{content: 增量字符串}`。
6. WHEN LLM_Client 收到一段包含 `reasoning_content` 增量的 chunk，THE App_Coordinator SHALL 向 Frontend_UI 发送 `llm-delta` 事件，载荷至少包含 `{reasoning: 增量字符串}`。
7. WHEN LLM_Stream 自然结束（收到 `data: [DONE]`），THE App_Coordinator SHALL 把累积的完整 `content` 与可选 `reasoning_content` 作为一条 `role="assistant"` 的 Message 追加到 Conversation_Store，并向 Frontend_UI 发送 `llm-done` 事件，载荷为 `{full_content: 完整内容字符串}`。
8. WHEN Frontend_UI 调用 `StopGeneration()`，THE App_Coordinator SHALL 取消当前 LLM_Stream 的 context、关闭底层 HTTP 连接，并把已累积的内容作为一条 `role="assistant"` Message 写入 Conversation_Store。
9. IF LLM_Client 收到 4xx 状态响应，THEN THE App_Coordinator SHALL 解析响应体中的错误信息并向 Frontend_UI 发送 `llm-error` 事件，载荷为 `{error: 错误消息}`，且不向 Conversation_Store 追加 assistant Message。
10. IF LLM_Stream 在自然结束前网络中断，THEN THE App_Coordinator SHALL 把已累积内容作为一条 `role="assistant"` Message 写入 Conversation_Store，并向 Frontend_UI 发送 `llm-error` 事件指示连接中断，使前端在该气泡末尾追加"（连接中断）"。
11. WHILE 一次 LLM_Stream 正在进行中时再次调用 `SendMessage(text)`，THE App_Coordinator SHALL 拒绝该调用并向 Frontend_UI 返回错误，确保同一时刻最多只有一个 LLM_Stream。

### 需求 6：会话历史与新建对话

**用户故事**：作为用户，我希望连续对话时模型记住上下文，并能随时清空开始新对话，以便管理思路。

#### 验收准则

1. WHEN MockAgent_App 启动或 `NewConversation()` 被调用，THE Conversation_Store SHALL 把内部 messages 列表重置为空；THE App_Coordinator SHALL 在下一次发送给 LLM 前把当前 System_Prompt 拼接为第一条 `role="system"` Message（若 System_Prompt 为空则不拼接）。
2. THE Conversation_Store SHALL 按追加顺序保存所有 user 与 assistant Message，使每次 `SendMessage` 调用都能取到完整历史。
3. WHEN Frontend_UI 调用 `NewConversation()`，THE App_Coordinator SHALL 取消任何进行中的 LLM_Stream，调用 Conversation_Store 重置，并通知 Frontend_UI 清空聊天列表。
4. THE Conversation_Store SHALL 不持久化历史到磁盘（重启后历史不恢复，与第一版范围一致）。

### 需求 7：导出对话

**用户故事**：作为用户，我希望把当前对话导出为 Markdown 或 JSON 文件，以便保存或分享。

#### 验收准则

1. WHEN Frontend_UI 调用 `ExportConversation("md")`，THE App_Coordinator SHALL 弹出系统保存对话框，默认文件名形如 `MockAgent-对话-YYYY-MM-DD-HHMM.md`。
2. WHEN Frontend_UI 调用 `ExportConversation("json")`，THE App_Coordinator SHALL 弹出系统保存对话框，默认文件名形如 `MockAgent-对话-YYYY-MM-DD-HHMM.json`。
3. WHEN 用户在对话框确认保存路径，THE App_Coordinator SHALL 将 Conversation_Store 当前 messages 写入该文件。
4. WHERE 导出格式为 `"md"`，THE App_Coordinator SHALL 把每条消息按 `## 你` 或 `## AI` 标题分段，原样保留 assistant Message 中的代码块。
5. WHERE 导出格式为 `"json"`，THE App_Coordinator SHALL 写入符合设计 6.4 `Message` 结构的完整 messages 数组，包含 `reasoning_content` 字段。
6. IF `format` 参数既不是 `"md"` 也不是 `"json"`，THEN THE App_Coordinator SHALL 返回错误且不弹出保存对话框。
7. THE App_Coordinator SHALL 保证 JSON 导出文件经 `json.Unmarshal` 解析后产生的 messages 数组与导出时的 Conversation_Store 内容元素相同（顺序与字段一致），即满足 JSON 序列化往返一致性。

### 需求 8：聊天 UI 与输入

**用户故事**：作为用户，我希望聊天界面清晰显示我和 AI 的消息、支持 Markdown 与代码高亮、支持流式打字效果与键盘操作，以便愉快地阅读与编辑。

#### 验收准则

1. THE Frontend_UI SHALL 把 `role="user"` 的消息以右对齐蓝色气泡渲染，把 `role="assistant"` 的消息以左对齐灰白气泡渲染。
2. WHEN Frontend_UI 渲染一条 assistant Message，THE Frontend_UI SHALL 用 `marked` 库把 `content` 渲染为 HTML，并用 `highlight.js` 给代码块上色。
3. WHEN Frontend_UI 渲染一个代码块，THE Frontend_UI SHALL 在该代码块右上角显示"复制"按钮；点击后把代码块原文写入系统剪贴板。
4. WHERE 一条 assistant Message 含非空 `reasoning_content`，THE Frontend_UI SHALL 在内容上方渲染一个默认折叠的"已思考 N 秒"块，用户点击后展开显示思考内容。
5. WHEN Frontend_UI 收到 `llm-delta` 事件，THE Frontend_UI SHALL 把对应增量追加到当前 assistant 气泡，呈现打字机效果，且滚动条若已贴底则保持贴底。
6. WHEN 用户在输入框按下 `Enter` 且未按 `Shift`，THE Frontend_UI SHALL 调用 `SendMessage(输入框文本)`。
7. WHEN 用户在输入框按下 `Shift+Enter`，THE Frontend_UI SHALL 在光标处插入换行而不发送。
8. THE Frontend_UI SHALL 在输入框文本超过单行时自动扩展高度，最多 6 行，超过后内部出现纵向滚动条。
9. WHILE LLM_Stream 进行中，THE Frontend_UI SHALL 把"发送"按钮替换为"停止生成"按钮；用户点击该按钮时调用 `StopGeneration()`。
10. THE Frontend_UI SHALL 在录音状态栏区分以下四种状态并显示对应文案：空闲（"🎤 按 F2 录音"）、录音中（红点闪烁 + 计时秒数）、识别中（"识别中..."）、错误（红字错误信息，3 秒后自动恢复）。
11. WHEN 用户在输入框右侧的 🎤 按钮上按下鼠标主键（mousedown），THE Frontend_UI SHALL 触发与 `OnPress` 等价的录音开始流程；松开（mouseup 或 mouseleave）时触发与 `OnRelease` 等价的录音结束流程。
12. THE Frontend_UI SHALL 提供标题栏按钮：📥 触发 `ExportConversation` 选择菜单（Markdown / JSON）、⚙ 触发 `OpenConfigFile()`、🗑 触发 `NewConversation()`。
13. THE Frontend_UI SHALL 默认采用深色主题，主窗口默认尺寸 900×700，最小尺寸 600×500。

### 需求 9：系统托盘

**用户故事**：作为用户，我希望关闭主窗口时程序仍在后台运行、热键依然有效，以便随时调用。

#### 验收准则

1. WHEN MockAgent_App 启动，THE Tray_Manager SHALL 在系统托盘区显示 MockAgent 图标。
2. THE Tray_Manager SHALL 提供"显示主窗口"、"新建对话"、"打开配置文件"、"退出"四个菜单项。
3. WHEN 用户点击主窗口标题栏的关闭按钮 [×]，THE App_Coordinator SHALL 隐藏主窗口而不退出主进程，且 Hotkey_Manager 与 Recorder 仍保持就绪状态。
4. WHEN 用户双击托盘图标或点击托盘菜单"显示主窗口"，THE Tray_Manager SHALL 通过线程安全方式调用 Wails runtime 显示并聚焦主窗口（事件 `tray-show-window`）。
5. WHEN 用户点击托盘菜单"新建对话"，THE Tray_Manager SHALL 触发与 `NewConversation()` 相同的清空流程。
6. WHEN 用户点击托盘菜单"打开配置文件"，THE Tray_Manager SHALL 触发与 `OpenConfigFile()` 相同的流程。
7. WHEN 用户点击托盘菜单"退出"，THE App_Coordinator SHALL 注销全局热键、停止任何进行中的 Recording_Session 与 LLM_Stream，并彻底退出主进程。
8. THE Tray_Manager SHALL 不在主线程阻塞 Wails 主循环；如使用 `getlantern/systray` 时其 `Run` 必须在独立 goroutine 中启动。

### 需求 10：错误处理与防护

**用户故事**：作为用户，我希望各种错误（缺配置、网络、设备等）都有清晰的反馈，且不会因为错误就让程序崩溃或泄露密钥。

#### 验收准则

1. IF 在调用 `SendMessage` 时 Config 中 `deepseek.api_key` 为空，THEN THE App_Coordinator SHALL 拒绝该调用并向 Frontend_UI 返回明确的"DeepSeek API Key 未配置"错误。
2. IF 在调用 `OnPress` 时 Config 中任一 `tencent.app_id`、`tencent.secret_id`、`tencent.secret_key` 为空，THEN THE App_Coordinator SHALL 阻止启动 ASR_Client 并向 Frontend_UI 显示"腾讯云凭证未配置"提示。
3. THE App_Coordinator SHALL 把所有 `recording`、`asr`、`llm` 阶段抛出的 panic 在协程边界处恢复（recover），转化为对应的 `*-error` 事件，避免主进程崩溃。
4. THE LLM_Client、ASR_Client、Config_Loader SHALL 在写日志或返回错误信息时不输出 Hidden_Field 的明文值。
5. WHEN App_Coordinator 处理任何 `*-error` 事件，THE App_Coordinator SHALL 在错误消息中保留可定位问题的错误码或简短描述（不含密钥）。
