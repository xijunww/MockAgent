# MockAgent 实施计划

**关联设计**：`2026-05-15-mockagent-design.md`
**关联需求**：`2026-05-15-mockagent-requirements.md`
**实现语言**：Go（后端） + 原生 HTML/CSS/JS（前端）

> 把功能设计转化为一系列代码生成提示，每个提示在前一步基础上增量推进，最终把所有部件接入主程序。本任务列表只关注涉及编写、修改或测试代码的任务。带 `*` 后缀的子任务为可选测试任务，编码代理在常规执行中可跳过；带具体属性编号的属性测试任务必须直接引用 design.md 中的属性。

## 任务

- [ ] 1. 搭建 Go module 与项目骨架
  - 在仓库根目录新建 `app/` 子模块（独立 `go.mod`），使用 `replace` 指向本地 `tencentcloud-speech-sdk-go`
  - 初始化 Wails v2 项目（`wails init -n MockAgent -t vanilla`），把生成的 `frontend/`、`main.go`、`app.go`、`wails.json` 落入 `app/` 目录
  - 创建 `internal/` 子包目录骨架：`config/`、`hotkey/`、`recorder/`、`asr/`、`llm/`、`conversation/`、`tray/`
  - 添加根目录 `config.example.json`（与设计 6.1 字段一致，全部使用占位值）与 `.gitignore` 中忽略 `config.json`
  - _Requirements: 1.1, 11 范围（项目结构）_

- [ ] 2. 实现配置加载模块 `internal/config`
  - [ ] 2.1 定义 `Config` 结构体与字段标签
    - 嵌套 `Tencent`、`DeepSeek`、`Audio` 子结构体，对应设计 6.1
    - 为 `secret_id`、`secret_key`、`api_key` 提供 `String()` 掩码方法
    - 实现 `func Load(dir string) (*Config, error)`：当 `config.json` 不存在时从 `config.example.json` 复制；解析后用环境变量覆盖
    - 实现 `func (c *Config) Validate() error`：必填字段检查（DeepSeek API Key、Tencent 凭证），返回结构化错误以便上层判断
    - _Requirements: 1.1, 1.2, 1.3, 1.8, 10.1, 10.2_

  - [ ]* 2.2 属性测试：首启拷贝
    - **属性 1：首次启动从示例配置拷贝**
    - **Validates: Requirement 1.1**
    - 用临时目录与生成器构造任意有效示例 JSON 内容，断言 Load 后 `config.json` 与 `config.example.json` 字节相等
    - 库：`testing/quick` 或 `pgregory.net/rapid`，迭代 ≥ 100

  - [ ]* 2.3 属性测试：合并覆盖语义
    - **属性 2：文件值与环境变量合并的覆盖语义**
    - **Validates: Requirements 1.3, 1.4**
    - 生成随机 file Config 与随机环境变量子集，断言每个被设置字段取环境值，未被设置字段取文件值
    - 迭代 ≥ 100

  - [ ]* 2.4 属性测试：敏感字段不泄露
    - **属性 5：敏感字段不泄露**
    - **Validates: Requirements 1.8, 10.4**
    - 生成 Config 含随机非平凡密钥字符串，断言 `c.String()`、`fmt.Sprintf("%v", c)`、错误信息中不包含原始密钥
    - 迭代 ≥ 100

  - [ ] 2.5 实现 `OpenConfigFile()` 与 `ReloadConfig()` 辅助函数
    - `OpenConfigFile`：通过 `exec.Command` 在 Windows 用 `cmd /c start`、在 macOS 用 `open`、在 Linux 用 `xdg-open` 打开 `config.json`
    - `ReloadConfig` 在 App_Coordinator 中实现协调（暂留接口给第 11 任务调用）
    - _Requirements: 1.4, 1.7_

- [ ] 3. 实现热键解析与去重器 `internal/hotkey`
  - [ ] 3.1 编写 `Spec` 解析器
    - 定义 `type Spec struct { Mods []string; Key string }` 与 `func ParseSpec(s string) (Spec, error)`、`func (s Spec) String() string`
    - 支持设计 6.3 的所有合法形式；非法输入返回带原因的错误
    - 大小写归一化（如 `ctrl+ALT+space` → `Ctrl+Alt+Space`）
    - _Requirements: 2.6, 2.7_

  - [ ]* 3.2 属性测试：Hotkey_Spec 解析与可逆性
    - **属性 7：Hotkey_Spec 解析与可逆性**
    - **Validates: Requirements 2.6, 2.7**
    - 合法生成器：随机修饰键子集 + 合法主键，断言 `format(parse(s))` 与 *s* 大小写归一化后相等
    - 非法生成器：未知键名、空字符串、单独修饰键、重复修饰键过多，断言 parse 返回错误
    - 迭代 ≥ 100

  - [ ] 3.3 实现按下/松开事件去重器
    - 纯函数 `Debouncer`：维护"是否处于按下"状态，输入 press/release 事件流，输出 `OnPress`、`OnRelease` 调用序列
    - 满足"重复 press 不再触发；最后一次 release 才触发 stop"
    - _Requirements: 2.4_

  - [ ]* 3.4 属性测试：去重器
    - **属性 6：热键事件去重器**
    - **Validates: Requirement 2.4**
    - 生成任意按下/松开事件序列，断言 `OnPress` 仅在首个 press 上触发，`OnRelease` 仅在最后一个 release 上触发
    - 迭代 ≥ 100

  - [ ] 3.5 实现 `Manager` 与 `golang.design/x/hotkey` 集成
    - `func (m *Manager) Register(spec Spec) error`、`Unregister()`、`SetCallbacks(onPress, onRelease)`
    - 内部启动一个 goroutine 监听 hotkey 通道，把事件喂给 Debouncer
    - 注册失败时返回错误供上层在状态栏告警
    - _Requirements: 2.1, 2.2, 2.3, 2.5_

- [ ] 4. 实现录音模块 `internal/recorder`
  - [ ] 4.1 用 `malgo` 编写 `Recorder`
    - 提供接口 `Recorder` 与 `MalgoRecorder` 实现：`Start() error`、`Stop() ([]byte, error)`、`Probe() (bool, error)`
    - 配置 16000 Hz、单声道、`s16` 格式；通过回调把数据 append 到内部 buffer
    - 内部 `sync.Mutex` 保证只有一个 Session 活跃
    - 使用接口允许测试时注入伪录音源
    - _Requirements: 3.1, 3.3, 3.4, 3.7, 3.8_

  - [ ]* 4.2 属性测试：缓冲不丢字节
    - **属性 8：录音缓冲不丢字节**
    - **Validates: Requirement 3.4**
    - 实现一个 `FakeRecorder` 把测试方注入的字节数组通过回调写入累积 buffer，断言 `Stop()` 返回字节序列等于注入序列
    - 迭代 ≥ 100

  - [ ]* 4.3 属性测试：单录音流不变量
    - **属性 9：单录音流不变量**
    - **Validates: Requirement 3.8**
    - 生成 Start/Stop 操作序列，断言活跃 Session 数始终 ≤ 1，处于 Recording 时收到 Start 调用应失败
    - 迭代 ≥ 100

  - [ ] 4.4 实现录音时长阈值判定
    - 函数 `func ShouldRecognize(byteLen int, sampleRate, channels, bytesPerSample, minDurationMs int) bool`
    - 在 App_Coordinator 收到 PCM 后调用，决定是否进入 ASR
    - _Requirements: 3.9_

  - [ ]* 4.5 属性测试：录音时长阈值
    - **属性 10：录音时长阈值判定**
    - **Validates: Requirement 3.9**
    - 生成随机字节长度，断言返回值与精确公式一致
    - 迭代 ≥ 100

- [ ] 5. 检查点：核心采集链路
  - 确保 1–4 节中所有非可选任务通过 `go test ./...`（不含 `*` 标记的测试可视情况运行）；如出现疑问询问用户。

- [ ] 6. 实现 ASR 模块 `internal/asr`
  - [ ] 6.1 封装腾讯云 `FlashRecognizer`
    - 接口 `Client` 与 `TencentClient` 实现：`Recognize(ctx context.Context, pcm []byte) (string, error)`
    - 内部使用 `tencentcloud-speech-sdk-go/asr.NewFlashRecognizer(appID, credential)`、`Recognize(req)`，参数：`EngSerViceType=16k_zh`、`VoiceFormat=pcm`、`SpeechData` 设为传入 PCM
    - 错误分类：网络错误、鉴权错误、配额错误、空结果，全部封装为 `*asr.Error`
    - 实现 `func (c *TencentClient) DispatchResult(text string) (write bool, normalized string)` 用于"空白结果不回填"判定
    - _Requirements: 4.3, 4.4, 4.6, 4.7, 10.2_

  - [ ]* 6.2 属性测试：空白结果不回填
    - **属性 11：ASR 空白结果不回填**
    - **Validates: Requirement 4.6**
    - 生成纯空白与含非空白的字符串，断言 `DispatchResult` 的 `write` 等于 `strings.TrimSpace(t) != ""`
    - 迭代 ≥ 100

  - [ ] 6.3 编写 ASR 适配层契约
    - 在 `App_Coordinator` 侧引入 `type ASRClient interface { Recognize(ctx, []byte) (string, error) }`，使用 mock 实现做单元测试
    - _Requirements: 4.1_

- [ ] 7. 实现 LLM 客户端 `internal/llm`
  - [ ] 7.1 定义请求/响应结构
    - `ChatRequest`、`ChatChunk`（与 OpenAI 兼容字段保持一致）、`Delta`、`Choice`
    - `Delta` 同时含 `Content string` 与 `ReasoningContent string`
    - _Requirements: 5.2, 5.3, 5.4_

  - [ ] 7.2 实现 `Client.Stream`
    - `func (c *Client) Stream(ctx, messages []Message) (<-chan Delta, error)`
    - 构造 POST 请求至 `${base_url}/chat/completions`；headers `Authorization: Bearer …`、`Content-Type: application/json`、`Accept: text/event-stream`
    - 启动 goroutine 读取响应，按 `\n\n` 分块、`data: ` 前缀剥离、解析 JSON；遇 `[DONE]` 关闭 channel
    - 4xx 时读取错误体并返回错误（不开 channel）
    - 支持 `ctx.Cancel()` 立即关闭底层连接
    - _Requirements: 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 5.8, 5.9, 5.10_

  - [ ]* 7.3 属性测试：请求构造不变量
    - **属性 13：LLM 请求构造的不变量**
    - **Validates: Requirements 5.2, 5.3**
    - 用 `httptest.Server` 接收请求，生成随机 Config + 随机 messages 数组，断言 URL、Authorization、body.model、body.stream、body.messages 满足契约
    - 迭代 ≥ 100

  - [ ]* 7.4 属性测试：SSE 解析连接律 + 累积
    - **属性 14：SSE 解析的连接律与累积**
    - **Validates: Requirements 5.5, 5.6, 5.7**
    - 生成任意 chunk 数组（任意分块、任意 content/reasoning 增量），序列化为 SSE 给假服务器，断言解析后 delta 拼接与累计结果与原始相等
    - 迭代 ≥ 100

  - [ ]* 7.5 属性测试：4xx 不污染会话
    - **属性 15：4xx 错误不污染会话**
    - **Validates: Requirement 5.9**
    - 生成随机 4xx 状态 + 错误体，断言 conversation 长度不增、收到 `llm-error`
    - 迭代 ≥ 100

- [ ] 8. 实现会话存储 `internal/conversation`
  - [ ] 8.1 定义 `Store`
    - `Message` 结构体与 6.4 一致；`Store.Append(msg Message)`、`Store.Reset(systemPrompt string)`、`Store.Snapshot() []Message`
    - 内部 `sync.RWMutex` 保护 messages slice
    - _Requirements: 6.1, 6.2_

  - [ ]* 8.2 属性测试：NewConversation 重置
    - **属性 17：NewConversation 重置**
    - **Validates: Requirement 6.1**
    - 生成空/非空 system_prompt 与任意已有 messages，断言 Reset 后 Snapshot 等于预期
    - 迭代 ≥ 100

  - [ ]* 8.3 属性测试：会话顺序保留
    - **属性 18：会话顺序保留**
    - **Validates: Requirement 6.2**
    - 生成 (role, content) 序列，依次 Append，断言 Snapshot 顺序与字段与序列一致
    - 迭代 ≥ 100

  - [ ] 8.4 实现 Markdown 与 JSON 导出
    - `Export(format string, messages []Message) (filename string, data []byte, err error)`
    - 文件名 `MockAgent-对话-YYYY-MM-DD-HHMM.{md|json}`
    - md：每条消息以 `## 你` 或 `## AI` 开头，原样保留代码块；json：`json.MarshalIndent(messages, "", "  ")`
    - 非法 format 返回错误
    - _Requirements: 7.1, 7.2, 7.4, 7.5, 7.6, 7.7_

  - [ ]* 8.5 属性测试：导出文件名格式
    - **属性 19：导出默认文件名格式**
    - **Validates: Requirements 7.1, 7.2**
    - 生成任意 `time.Time` 与 format∈{"md","json"}，断言文件名匹配正则
    - 迭代 ≥ 100

  - [ ]* 8.6 属性测试：Markdown 导出结构
    - **属性 20：Markdown 导出结构保真**
    - **Validates: Requirement 7.4**
    - 生成 messages 列表，断言 `## 你` / `## AI` 出现次数与 user/assistant 数一致，且代码块原文出现在输出中
    - 迭代 ≥ 100

  - [ ]* 8.7 属性测试：JSON 导出 round-trip
    - **属性 21：JSON 导出往返一致**
    - **Validates: Requirements 7.5, 7.7**
    - 生成 messages 列表，导出 JSON 后再 Unmarshal，断言与原始相等
    - 迭代 ≥ 100

  - [ ]* 8.8 属性测试：非法格式拒绝
    - **属性 22：非法导出格式被拒绝**
    - **Validates: Requirement 7.6**
    - 生成不在 {"md","json"} 的随机字符串，断言返回错误且未生成数据
    - 迭代 ≥ 100

- [ ] 9. 检查点：所有后端纯逻辑模块
  - 运行 `go test ./internal/...`，确保所有非可选测试通过；如出现疑问询问用户。

- [ ] 10. 实现系统托盘 `internal/tray`
  - [ ] 10.1 用 `getlantern/systray` 在独立 goroutine 中启动
    - `Manager.Run(ctx, callbacks)`：在 goroutine 中调用 `systray.Run(onReady, onExit)`
    - 菜单项：显示主窗口、新建对话、打开配置文件、退出
    - 回调通过线程安全 channel 转发到 App_Coordinator
    - _Requirements: 9.1, 9.2, 9.4, 9.5, 9.6, 9.7, 9.8_

  - [ ] 10.2 提供托盘图标资源
    - 内嵌 PNG/ICO 图标到二进制（`//go:embed`）
    - _Requirements: 9.1_

- [ ] 11. 实现 App_Coordinator（`app.go`）与 Wails 绑定
  - [ ] 11.1 定义 App 类型与生命周期
    - `App.startup(ctx)`：保存 Wails ctx；初始化 Config_Loader、Hotkey_Manager、Recorder、ASR_Client、LLM_Client、Conversation_Store、Tray_Manager
    - 启动时按钩子链路注册：`Hotkey.OnPress` → `App.onPress`、`Hotkey.OnRelease` → `App.onRelease`
    - 实现 panic 恢复包装：`safeGo(fn func() error)` 把任一阶段的 panic 转化为对应 `*-error` 事件
    - _Requirements: 1.1, 1.2, 2.1, 3.1, 9.3, 10.3_

  - [ ] 11.2 实现录音 → ASR → 输入框 流水线
    - `onPress`：校验 tencent 凭证非空、调用 `Recorder.Start`、发 `recording-started`；缺凭证则发提示事件并不启动
    - `onRelease`：调用 `Recorder.Stop` → 应用 `ShouldRecognize` 阈值 → 发 `recording-stopped` → 异步调用 `ASR_Client.Recognize` → 根据 `DispatchResult` 决定发 `asr-result` 或提示事件
    - 维护"当前 ASR session id"以实现"新 Press 取消旧 ASR"
    - _Requirements: 3.5, 3.6, 3.7, 3.9, 4.1, 4.2, 4.4, 4.5, 4.6, 4.7, 4.8, 10.2_

  - [ ]* 11.3 属性测试：新 Press 取消旧 ASR
    - **属性 12：新一轮按下取消上一轮 ASR**
    - **Validates: Requirement 4.8**
    - 用 mock Recorder 与 mock ASR（注入完成时延），生成 Press/Release/ASRComplete 事件序列，断言被回填的文本来自最后一个 Recording_Session
    - 迭代 ≥ 100

  - [ ] 11.4 实现 SendMessage 与流式响应
    - `SendMessage(text string) error`：校验 DeepSeek API Key 非空与无活跃流；追加 user 消息；启动 goroutine 调用 `LLM_Client.Stream`；解析 delta 通道并发 `llm-delta`；流尾 `llm-done` 时把累积内容追加为 assistant 消息；4xx 发 `llm-error` 不追加；中断发 `llm-error` 并把已累积写入
    - `StopGeneration()`：取消当前流 ctx；把已累积内容写入 conversation
    - 用 `sync.Mutex` 保证最多一个活跃流；活跃时拒绝新的 `SendMessage`
    - _Requirements: 5.1, 5.5, 5.6, 5.7, 5.8, 5.9, 5.10, 5.11, 10.1_

  - [ ]* 11.5 属性测试：单 LLM 流不变量
    - **属性 16：单 LLM 流不变量**
    - **Validates: Requirement 5.11**
    - 生成 SendMessage / StreamComplete / StopGeneration 操作序列，断言活跃流数 ≤ 1
    - 迭代 ≥ 100

  - [ ]* 11.6 属性测试：缺凭证拒绝外部调用
    - **属性 27：缺凭证时拒绝外部调用**
    - **Validates: Requirements 10.1, 10.2**
    - 用 mock LLM_Client/ASR_Client，生成缺凭证的 Config + 任意输入文本，断言 SendMessage 报错且 LLM 未发请求；OnPress 不调用 ASR
    - 迭代 ≥ 100

  - [ ] 11.7 实现 NewConversation / GetConfig / OpenConfigFile / ReloadConfig / ExportConversation
    - `NewConversation`：取消进行中的 LLM_Stream，重置 Conversation_Store，发事件清空前端
    - `GetConfig`：返回掩码后的 Config 视图
    - `ReloadConfig`：重载 Config；如果 hotkey 变化则注销旧 spec 并注册新 spec；不打断进行中的录音/流
    - `ExportConversation`：调用 `runtime.SaveFileDialog` 取得路径，写文件
    - _Requirements: 1.4, 1.5, 1.6, 1.7, 6.3, 7.1, 7.2, 7.3_

  - [ ]* 11.8 属性测试：热键变更时重新注册
    - **属性 3：热键变更时重新注册**
    - **Validates: Requirement 1.5**
    - 用 mock Hotkey_Manager 计录调用次数，对随机 (oldSpec, newSpec) 验证 register/unregister 调用次数
    - 迭代 ≥ 100

  - [ ]* 11.9 属性测试：重载不中断进行中的录音与流
    - **属性 4：重载不中断进行中的录音与流**
    - **Validates: Requirement 1.6**
    - 用 fake Recorder/Stream，在不同时间点触发 ReloadConfig，断言活跃状态与已累积内容不变
    - 迭代 ≥ 100

  - [ ] 11.10 接通系统托盘
    - 把托盘菜单回调连接到 NewConversation、OpenConfigFile、Wails `runtime.WindowShow`、彻底退出（取消所有 ctx + `runtime.Quit`）
    - 主窗口关闭事件设置为隐藏（`OnBeforeClose` 返回 true 后 `runtime.WindowHide`）
    - _Requirements: 9.3, 9.4, 9.5, 9.6, 9.7_

  - [ ]* 11.11 属性测试：关闭主窗口后热键仍就绪
    - **属性 26：关闭主窗口后热键仍就绪**
    - **Validates: Requirement 9.3**
    - 用虚拟事件注入器：模拟"关闭主窗口 → 按下 → 松开"，断言 OnPress/OnRelease 仍被触发
    - 迭代 ≥ 100

- [ ] 12. 检查点：后端集成
  - 运行 `go test ./...` 确保后端非可选测试通过；如出现疑问询问用户。

- [ ] 13. 实现前端 `frontend/`
  - [ ] 13.1 搭建 HTML/CSS/JS 骨架与样式
    - `index.html`：标题栏（导出/配置/新对话按钮）、聊天列表 `#chat-list`、状态栏 `#recording-status`、输入区（`textarea`、麦克风按钮、发送/停止按钮）
    - `src/style.css`：深色主题、用户/AI 气泡、状态栏样式、6 行高度上限的 `textarea`
    - 默认窗口尺寸 900×700，最小 600×500（在 `wails.json`）
    - _Requirements: 8.1, 8.13_

  - [ ] 13.2 引入 Markdown 与代码高亮
    - 通过 npm/yarn 安装 `marked` 与 `highlight.js`；在 `main.js` 中渲染 assistant 消息
    - 给每个代码块挂"复制"按钮，点击写入剪贴板（`navigator.clipboard.writeText`）
    - 折叠的 reasoning_content 块（默认折叠，显示"已思考 N 秒"）
    - _Requirements: 8.2, 8.3, 8.4_

  - [ ]* 13.3 属性测试：代码块复制保真
    - **属性 23：代码块复制保真**
    - **Validates: Requirement 8.3**
    - 用 Vitest + `fast-check`：生成任意代码字符串，模拟点击复制按钮，mock 剪贴板 API，断言剪贴板内容严格等于原代码
    - 迭代 ≥ 100

  - [ ] 13.4 实现输入框行为
    - Enter 发送，Shift+Enter 换行
    - 文本超过单行时根据 `scrollHeight` 自动扩展，最多 6 行后内部滚动
    - 空白文本不发送
    - _Requirements: 8.6, 8.7, 8.8_

  - [ ]* 13.5 属性测试：输入框 6 行高度上限
    - **属性 24：输入框高度上限**
    - **Validates: Requirement 8.8**
    - 生成任意行数 *n*，断言 `computeHeight(n, lineHeight)` 等于 `min(n, 6) * lineHeight` 且当 n>6 时启用滚动
    - 迭代 ≥ 100

  - [ ] 13.6 实现录音状态栏与麦克风按钮
    - 监听 Wails 事件 `recording-started`、`recording-stopped`、`asr-progress`、`asr-result`、`asr-error` 切换状态文案与样式
    - 麦克风按钮：mousedown 调用 `App.OnPress`、mouseup/mouseleave 调用 `App.OnRelease`（暴露为 Wails 绑定方法或共用 `SimulatePress/SimulateRelease`）
    - 录音中显示红点闪烁与计时器
    - _Requirements: 8.10, 8.11, 4.2_

  - [ ] 13.7 实现流式渲染与"停止生成"
    - 监听 `llm-delta` 把 content 与 reasoning 分别追加到当前 assistant 气泡
    - 渲染前判断滚动条是否贴底，处理后据此决定是否保持贴底
    - 流进行中把"发送"按钮替换为"停止生成"，点击调用 `App.StopGeneration`
    - _Requirements: 5.5, 5.6, 5.7, 8.5, 8.9_

  - [ ]* 13.8 属性测试：滚动贴底保持
    - **属性 25：流式渲染时滚动贴底保持**
    - **Validates: Requirement 8.5**
    - 用 jsdom + Vitest：mock 滚动状态与 delta 事件，断言贴底状态被保持/不被强制
    - 迭代 ≥ 100

  - [ ] 13.9 标题栏按钮联动
    - 📥 触发 ExportConversation 选择 md/json；⚙ 触发 OpenConfigFile；🗑 触发 NewConversation（带确认）
    - _Requirements: 7.1, 7.2, 8.12_

  - [ ] 13.10 错误处理与提示
    - 监听 `llm-error`、`asr-error`、其他提示事件，状态栏 3 秒红字；`llm-error` 时若已有部分内容，气泡末尾追加"（连接中断）"
    - _Requirements: 4.7, 5.10, 8.10_

- [ ] 14. 主入口 `main.go` 与构建
  - 在 `main.go` 中：调用 `config.Load`、构造 App、调用 `wails.Run` 配置（标题、尺寸、`OnBeforeClose` 隐藏窗口、绑定 App 方法）
  - 在独立 goroutine 中启动 Tray_Manager
  - 在 `wails.json` 中配置应用名、图标、构建脚本
  - _Requirements: 1.1, 1.2, 9.3_

- [ ] 15. 端到端流水线接入
  - 把 onPress/onRelease 与 SendMessage 流程在 App 中串联完毕
  - 把所有 Wails 事件名（recording-started/recording-stopped/asr-progress/asr-result/asr-error/llm-delta/llm-done/llm-error/tray-show-window）统一定义到常量文件并在前端订阅
  - 实现"麦克风按钮等价于 F2"：UI 调用同一组协调方法
  - _Requirements: 6.5（事件契约整体）, 8.11_

- [ ] 16. 最终检查点
  - 运行 `go test ./...` 与（若启用前端测试）`npm test` 确保所有非可选测试通过
  - 用占位密钥构建 `wails build` 验证编译产物正常生成
  - 如出现疑问询问用户

## 备注

- 标记 `*` 的子任务为可选测试任务；编码代理在常规执行中不实现这些任务
- 顶层任务不携带 `*` 后缀
- 每个属性测试任务直接引用 design.md 第 11 节"正确性属性"中的属性编号与对应需求条目
- 检查点（任务 5、9、12、16）仅做整体验证，不引入新代码
