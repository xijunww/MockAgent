import './style.css';
import 'highlight.js/styles/github-dark.css';

import { marked } from 'marked';
import hljs from 'highlight.js';

import {
  SendMessage, StopGeneration, NewConversation, GetConfig,
  OpenConfigFile, ReloadConfig, ExportConversation,
  SimulatePress, SimulateRelease,
} from '../wailsjs/go/main/App.js';
import { EventsOn } from '../wailsjs/runtime/runtime.js';

// ==================== 全局状态 ====================
const state = {
  /** 当前正在流式输出的 AI 气泡 DOM 与累积内容（避免每帧重渲染整段 markdown）。 */
  streamingBubble: null,
  streamingContent: '',
  streamingReasoning: '',
  streamingReasoningEl: null,
  streamingThinkStart: 0,
  /** 录音计时定时器。 */
  recordingTimer: null,
  recordingStart: 0,
  /** 状态栏出错时回到空闲的定时器。 */
  statusResetTimer: null,
  /** 当前快捷键名（来自 GetConfig）。 */
  hotkey: 'F2',
};

// ==================== marked 配置 ====================
marked.use({
  breaks: true,
  gfm: true,
  renderer: {
    code(code, lang) {
      let html;
      if (lang && hljs.getLanguage(lang)) {
        try {
          html = hljs.highlight(code, { language: lang }).value;
        } catch (_) {
          html = escapeHtml(code);
        }
      } else {
        html = hljs.highlightAuto(code).value;
      }
      const safeOriginal = escapeHtml(code);
      return `<pre data-code="${encodeURIComponent(code)}"><button class="code-copy" type="button">复制</button><code class="hljs ${lang ? 'language-' + lang : ''}">${html}</code></pre>`;
      // 真实代码留在 data-code（URI 编码），复制按钮取出原文
      // safeOriginal 防 XSS 不需要单独使用，但保留逻辑路径
      void safeOriginal;
    },
  },
});

function escapeHtml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// ==================== DOM 引用 ====================
const $ = (id) => document.getElementById(id);
const chatList = $('chat-list');
const input = $('input');
const sendBtn = $('btn-send');
const micBtn = $('btn-mic');
const banner = $('banner');
const statusBar = $('recording-status');
const statusIcon = $('status-icon');
const statusText = $('status-text');
const statusHotkey = $('status-hotkey');
const kbdHotkey = $('kbd-hotkey');
const exportMenu = $('export-menu');

// ==================== 启动加载 ====================
loadConfigStatus();

async function loadConfigStatus() {
  try {
    const cfg = await GetConfig();
    if (cfg.error) {
      showBanner(cfg.error, 'error');
      return;
    }
    if (cfg.hotkey) {
      state.hotkey = cfg.hotkey;
      kbdHotkey.textContent = cfg.hotkey;
      statusHotkey.textContent = cfg.hotkey;
    }
    if (!cfg.api_key_set) {
      showBanner('DeepSeek API Key 未配置，无法发送消息（点击 ⚙ 打开 config.json）', 'warning');
    } else if (!cfg.tencent || !cfg.tencent.app_id) {
      showBanner('腾讯云语音凭证未配置，无法录音转文字（点击 ⚙ 打开 config.json）', 'warning');
    } else {
      hideBanner();
    }
  } catch (e) {
    showBanner('加载配置失败: ' + (e?.message || e), 'error');
  }
}

function showBanner(text, kind = 'error') {
  banner.textContent = text;
  banner.className = 'banner' + (kind === 'warning' ? ' warning' : '');
}
function hideBanner() {
  banner.classList.add('hidden');
}

// ==================== 状态栏 ====================
function setStatus(kind, iconHtml, text) {
  if (state.statusResetTimer) {
    clearTimeout(state.statusResetTimer);
    state.statusResetTimer = null;
  }
  statusBar.className = 'recording-status ' + kind;
  statusIcon.textContent = iconHtml;
  if (typeof text === 'string') {
    statusText.innerHTML = text;
  }
}

function setStatusIdle() {
  setStatus('idle', '🎤', `按 <kbd class="inline-kbd">${escapeHtml(state.hotkey)}</kbd> 录音`);
}
setStatusIdle();

function flashStatus(kind, icon, text, ms = 3000) {
  setStatus(kind, icon, text);
  state.statusResetTimer = setTimeout(setStatusIdle, ms);
}

// ==================== 录音事件 ====================
EventsOn('recording-started', () => {
  state.recordingStart = Date.now();
  micBtn.classList.add('recording');
  setStatus('recording', '●', '录音中 0.0s');
  state.recordingTimer = setInterval(() => {
    const sec = ((Date.now() - state.recordingStart) / 1000).toFixed(1);
    statusText.textContent = `录音中 ${sec}s`;
  }, 100);
});

EventsOn('recording-stopped', () => {
  micBtn.classList.remove('recording');
  if (state.recordingTimer) {
    clearInterval(state.recordingTimer);
    state.recordingTimer = null;
  }
  setStatusIdle();
});

EventsOn('asr-progress', () => {
  setStatus('recognizing', '✏', '识别中...');
});

EventsOn('asr-result', ({ text }) => {
  setStatusIdle();
  if (typeof text === 'string' && text.trim() !== '') {
    appendInputText(text);
  }
});

EventsOn('asr-error', ({ error }) => {
  flashStatus('error', '❌', '识别错误：' + (error || '未知'));
});

EventsOn('asr-notice', ({ message }) => {
  flashStatus('notice', 'ℹ', message || '');
});

EventsOn('config-status', (payload) => {
  if (payload && payload.ok === false) {
    showBanner(payload.error || '配置异常', 'warning');
  } else {
    hideBanner();
    loadConfigStatus();
  }
});

EventsOn('conversation-cleared', () => {
  chatList.innerHTML = '';
  const welcome = document.createElement('div');
  welcome.className = 'welcome';
  welcome.innerHTML = `按住快捷键 <kbd>${escapeHtml(state.hotkey)}</kbd> 录音并转文字，或在输入框按住 🎤 按钮。`;
  chatList.appendChild(welcome);
});

// ==================== 输入框 ====================
function appendInputText(text) {
  if (input.value.trim() === '') {
    input.value = text;
  } else {
    input.value += (input.value.endsWith(' ') || input.value.endsWith('\n') ? '' : ' ') + text;
  }
  autoresize();
  input.focus();
}

function autoresize() {
  input.style.height = 'auto';
  const lh = parseFloat(getComputedStyle(input).lineHeight) || 22;
  const maxH = lh * 6 + 22;
  input.style.height = Math.min(input.scrollHeight, maxH) + 'px';
  input.style.overflowY = input.scrollHeight > maxH ? 'auto' : 'hidden';
}
input.addEventListener('input', autoresize);

input.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault();
    handleSend();
  }
});

// ==================== 发送 / 停止 ====================
sendBtn.addEventListener('click', () => {
  if (sendBtn.classList.contains('stop')) {
    StopGeneration();
  } else {
    handleSend();
  }
});

async function handleSend() {
  const text = input.value.trim();
  if (text === '') return;
  appendUserBubble(text);
  startStreamingBubble();
  input.value = '';
  autoresize();
  setSendingMode(true);
  try {
    await SendMessage(text);
  } catch (e) {
    setSendingMode(false);
    finalizeStreamingAsError(String(e?.message || e || '发送失败'));
  }
}

function setSendingMode(isSending) {
  if (isSending) {
    sendBtn.classList.add('stop');
    sendBtn.textContent = '停止生成';
  } else {
    sendBtn.classList.remove('stop');
    sendBtn.textContent = '发送';
  }
}

// ==================== LLM 流 ====================
EventsOn('llm-delta', (payload) => {
  if (!state.streamingBubble) startStreamingBubble();
  if (payload.reasoning) {
    state.streamingReasoning += payload.reasoning;
    updateReasoningBlock();
  }
  if (payload.content) {
    state.streamingContent += payload.content;
    updateStreamingContent();
  }
});

EventsOn('llm-done', () => {
  setSendingMode(false);
  finalizeStreaming();
});

EventsOn('llm-error', (payload) => {
  setSendingMode(false);
  if (state.streamingBubble && (state.streamingContent || state.streamingReasoning)) {
    // 流中途出错：保留部分内容，追加提示
    state.streamingContent += `\n\n_（${escapeHtml(payload.error || '连接中断')}）_`;
    updateStreamingContent();
    finalizeStreaming();
  } else {
    finalizeStreamingAsError(payload?.error || '请求失败');
  }
});

function appendUserBubble(text) {
  removeWelcome();
  const el = document.createElement('div');
  el.className = 'bubble user';
  el.textContent = text;
  chatList.appendChild(el);
  scrollToBottomIfNearBottom(true);
}

function startStreamingBubble() {
  removeWelcome();
  const el = document.createElement('div');
  el.className = 'bubble ai';
  const reasoningWrap = document.createElement('div');
  reasoningWrap.className = 'reasoning';
  reasoningWrap.style.display = 'none';
  reasoningWrap.innerHTML = `
    <div class="reasoning-toggle">思考中...</div>
    <div class="reasoning-content"></div>
  `;
  reasoningWrap.querySelector('.reasoning-toggle').addEventListener('click', () => {
    reasoningWrap.classList.toggle('open');
  });
  const contentEl = document.createElement('div');
  contentEl.className = 'ai-content';
  el.appendChild(reasoningWrap);
  el.appendChild(contentEl);
  chatList.appendChild(el);
  attachCodeCopyHandlers(el);

  state.streamingBubble = el;
  state.streamingContent = '';
  state.streamingReasoning = '';
  state.streamingReasoningEl = reasoningWrap;
  state.streamingThinkStart = Date.now();
  scrollToBottomIfNearBottom(true);
}

function updateStreamingContent() {
  if (!state.streamingBubble) return;
  const contentEl = state.streamingBubble.querySelector('.ai-content');
  if (!contentEl) return;
  const wasNearBottom = isNearBottom();
  contentEl.innerHTML = marked.parse(state.streamingContent);
  attachCodeCopyHandlers(contentEl);
  if (wasNearBottom) chatList.scrollTop = chatList.scrollHeight;
}

function updateReasoningBlock() {
  const wrap = state.streamingReasoningEl;
  if (!wrap) return;
  wrap.style.display = '';
  const elapsed = Math.max(1, Math.round((Date.now() - state.streamingThinkStart) / 1000));
  wrap.querySelector('.reasoning-toggle').textContent = `已思考 ${elapsed} 秒`;
  wrap.querySelector('.reasoning-content').textContent = state.streamingReasoning;
}

function finalizeStreaming() {
  state.streamingBubble = null;
  state.streamingContent = '';
  state.streamingReasoning = '';
  state.streamingReasoningEl = null;
}

function finalizeStreamingAsError(msg) {
  if (state.streamingBubble && !state.streamingContent && !state.streamingReasoning) {
    state.streamingBubble.remove();
  } else if (state.streamingBubble) {
    state.streamingContent += `\n\n_（${msg}）_`;
    updateStreamingContent();
  }
  finalizeStreaming();
  // 同时把错误投递到状态栏
  flashStatus('error', '❌', escapeHtml(msg));
}

function removeWelcome() {
  const w = chatList.querySelector('.welcome');
  if (w) w.remove();
}

// ==================== 滚动 ====================
function isNearBottom() {
  return chatList.scrollHeight - chatList.scrollTop - chatList.clientHeight < 40;
}
function scrollToBottomIfNearBottom(force = false) {
  if (force || isNearBottom()) {
    chatList.scrollTop = chatList.scrollHeight;
  }
}

// ==================== 代码复制 ====================
function attachCodeCopyHandlers(scope) {
  const buttons = scope.querySelectorAll('.code-copy');
  buttons.forEach((btn) => {
    if (btn.dataset.bound === '1') return;
    btn.dataset.bound = '1';
    btn.addEventListener('click', () => {
      const pre = btn.closest('pre');
      const raw = decodeURIComponent(pre?.dataset?.code || '');
      navigator.clipboard.writeText(raw).then(
        () => {
          btn.textContent = '已复制';
          btn.classList.add('copied');
          setTimeout(() => {
            btn.textContent = '复制';
            btn.classList.remove('copied');
          }, 1500);
        },
        () => {
          btn.textContent = '失败';
          setTimeout(() => (btn.textContent = '复制'), 1500);
        },
      );
    });
  });
}

// ==================== 麦克风按钮 ====================
const micPress = (e) => {
  e.preventDefault();
  if (!micBtn.classList.contains('recording')) SimulatePress();
};
const micRelease = (e) => {
  e.preventDefault();
  if (micBtn.classList.contains('recording')) SimulateRelease();
};
micBtn.addEventListener('mousedown', micPress);
micBtn.addEventListener('mouseup', micRelease);
micBtn.addEventListener('mouseleave', (e) => {
  if (micBtn.classList.contains('recording')) micRelease(e);
});
// 触屏支持
micBtn.addEventListener('touchstart', micPress, { passive: false });
micBtn.addEventListener('touchend', micRelease);

// ==================== 标题栏按钮 ====================
$('btn-config').addEventListener('click', () => {
  OpenConfigFile().catch((e) => showBanner('打开配置失败: ' + e, 'error'));
});

$('btn-reload').addEventListener('click', async () => {
  try {
    await ReloadConfig();
    flashStatus('notice', '✓', '配置已重载', 1500);
  } catch (e) {
    showBanner('重载失败: ' + (e?.message || e), 'error');
  }
});

$('btn-new').addEventListener('click', () => {
  if (chatList.querySelector('.bubble')) {
    if (!confirm('清空当前对话？')) return;
  }
  NewConversation();
});

$('btn-export').addEventListener('click', (e) => {
  e.stopPropagation();
  exportMenu.classList.toggle('hidden');
});
exportMenu.querySelectorAll('button').forEach((btn) => {
  btn.addEventListener('click', async () => {
    exportMenu.classList.add('hidden');
    try {
      await ExportConversation(btn.dataset.format);
      flashStatus('notice', '✓', '导出完成', 1500);
    } catch (e) {
      flashStatus('error', '❌', '导出失败: ' + (e?.message || e));
    }
  });
});
document.addEventListener('click', (e) => {
  if (!exportMenu.contains(e.target) && e.target.id !== 'btn-export') {
    exportMenu.classList.add('hidden');
  }
});
