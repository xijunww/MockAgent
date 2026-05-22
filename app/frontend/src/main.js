import './style.css';
import 'highlight.js/styles/github-dark.css';

import { marked } from 'marked';
import hljs from 'highlight.js';

import {
  SendMessage, StopGeneration, NewConversation, GetConfig,
  OpenConfigFile, ReloadConfig, ExportConversation,
  SimulatePress, SimulateRelease, UpdateHotkey,
  ListDocuments, AddDocument, RemoveDocument, SetDocumentEnabled, GetDocumentPreview,
  GetSystemPromptState, SaveSystemPrompt, DeleteSystemPromptHistory,
} from '../wailsjs/go/main/App.js';
import { EventsOn } from '../wailsjs/runtime/runtime.js';

// ==================== 全局状态 ====================
const state = {
  streamingBubble: null,
  streamingContent: '',
  streamingReasoning: '',
  streamingReasoningEl: null,
  streamingThinkStart: 0,
  recordingTimer: null,
  recordingStart: 0,
  statusResetTimer: null,
  recordHotkey: 'F2',
  sendHotkey: 'F4',
  systemHotkey: 'F3',
};

// ==================== marked ====================
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
      return `<pre data-code="${encodeURIComponent(code)}"><button class="code-copy" type="button">复制</button><code class="hljs ${lang ? 'language-' + lang : ''}">${html}</code></pre>`;
    },
  },
});

function escapeHtml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// ==================== DOM ====================
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
const kbdSendHotkey = $('kbd-send-hotkey');
const kbdSystemHotkey = $('kbd-system-hotkey');
const exportMenu = $('export-menu');
const configMenu = $('config-menu');
const menuRecordHotkey = $('menu-record-hotkey');
const menuSendHotkey = $('menu-send-hotkey');
const menuSystemHotkey = $('menu-system-hotkey');
const hotkeyModal = $('hotkey-modal');
const hotkeyModalTitle = $('hotkey-modal-title');
const hotkeyCapture = $('hotkey-capture');
const hotkeyError = $('hotkey-error');

// ==================== 启动加载 ====================
loadConfigStatus();

async function loadConfigStatus() {
  try {
    const cfg = await GetConfig();
    if (cfg.error) {
      showBanner(cfg.error, 'error');
      return;
    }
    if (cfg.record_hotkey) state.recordHotkey = cfg.record_hotkey;
    if (cfg.send_hotkey) state.sendHotkey = cfg.send_hotkey;
    if (cfg.system_hotkey) state.systemHotkey = cfg.system_hotkey;
    refreshHotkeyDisplay();

    if (!cfg.api_key_set) {
      showBanner('DeepSeek API Key 未配置，无法发送消息（点击 ⚙ → 打开配置文件）', 'warning');
    } else if (!cfg.tencent || !cfg.tencent.app_id) {
      showBanner('腾讯云语音凭证未配置，无法录音转文字（点击 ⚙ → 打开配置文件）', 'warning');
    } else {
      hideBanner();
    }
  } catch (e) {
    showBanner('加载配置失败: ' + (e?.message || e), 'error');
  }
}

function refreshHotkeyDisplay() {
  if (kbdHotkey) kbdHotkey.textContent = state.recordHotkey;
  if (kbdSendHotkey) kbdSendHotkey.textContent = state.sendHotkey;
  if (kbdSystemHotkey) kbdSystemHotkey.textContent = state.systemHotkey;
  if (statusHotkey) statusHotkey.textContent = state.recordHotkey;
  if (menuRecordHotkey) menuRecordHotkey.textContent = state.recordHotkey;
  if (menuSendHotkey) menuSendHotkey.textContent = state.sendHotkey;
  if (menuSystemHotkey) menuSystemHotkey.textContent = state.systemHotkey;
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
  setStatus('idle', '🎤', `按 <kbd class="inline-kbd">${escapeHtml(state.recordHotkey)}</kbd> 录音`);
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

EventsOn('hotkey-changed', (payload) => {
  if (payload?.record_hotkey) state.recordHotkey = payload.record_hotkey;
  if (payload?.send_hotkey) state.sendHotkey = payload.send_hotkey;
  if (payload?.system_hotkey) state.systemHotkey = payload.system_hotkey;
  refreshHotkeyDisplay();
  setStatusIdle();
});

EventsOn('send-hotkey-pressed', () => {
  // 输入框为空 → 静默忽略（按需求 1.A）
  if (input.value.trim() === '') return;
  // 已经在生成中 → 拒绝并提示
  if (sendBtn.classList.contains('stop')) {
    flashStatus('error', '❌', 'AI 正在回复中', 1500);
    return;
  }
  handleSend();
});

EventsOn('conversation-cleared', () => {
  chatList.innerHTML = '';
  const welcome = document.createElement('div');
  welcome.className = 'welcome';
  welcome.innerHTML = `按住 <kbd>${escapeHtml(state.recordHotkey)}</kbd> 录自己说话；按住 <kbd>${escapeHtml(state.systemHotkey)}</kbd> 录系统正在播放的声音（面试官 / 会议）。<br>按 <kbd>${escapeHtml(state.sendHotkey)}</kbd> 直接发送当前输入框内容。`;
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
micBtn.addEventListener('touchstart', micPress, { passive: false });
micBtn.addEventListener('touchend', micRelease);

// ==================== 标题栏按钮 ====================
$('btn-config').addEventListener('click', (e) => {
  e.stopPropagation();
  exportMenu.classList.add('hidden');
  configMenu.classList.toggle('hidden');
});

configMenu.querySelectorAll('button').forEach((btn) => {
  btn.addEventListener('click', async () => {
    configMenu.classList.add('hidden');
    const action = btn.dataset.action;
    if (action === 'open-config') {
      try { await OpenConfigFile(); } catch (e) { showBanner('打开配置失败: ' + e, 'error'); }
    } else if (action === 'reload-config') {
      try { await ReloadConfig(); flashStatus('notice', '✓', '配置已重载', 1500); }
      catch (e) { showBanner('重载失败: ' + (e?.message || e), 'error'); }
    } else if (action === 'edit-record-hotkey') {
      openHotkeyModal('record');
    } else if (action === 'edit-system-hotkey') {
      openHotkeyModal('system');
    } else if (action === 'edit-send-hotkey') {
      openHotkeyModal('send');
    } else if (action === 'edit-system-prompt') {
      openPromptModal();
    } else if (action === 'manage-docs') {
      openDocsModal();
    }
  });
});

$('btn-new').addEventListener('click', () => {
  if (chatList.querySelector('.bubble')) {
    if (!confirm('清空当前对话？')) return;
  }
  NewConversation();
});

$('btn-export').addEventListener('click', (e) => {
  e.stopPropagation();
  configMenu.classList.add('hidden');
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
  if (!configMenu.contains(e.target) && e.target.id !== 'btn-config') {
    configMenu.classList.add('hidden');
  }
});

// ==================== 修改快捷键弹窗 ====================
let hotkeyKind = null;          // 'record' | 'send' | 'system'
let captured = null;            // { mods: Set<string>, key: string }
let commitTimer = null;

const HOTKEY_KIND_LABEL = { record: '录音', send: '发送', system: '系统声音' };

function openHotkeyModal(kind) {
  hotkeyKind = kind;
  const label = HOTKEY_KIND_LABEL[kind] || '快捷键';
  hotkeyModalTitle.textContent = `设置${label}热键`;
  resetCapture();
  hotkeyModal.classList.remove('hidden');
  // 等下一帧让 DOM 出现后聚焦
  requestAnimationFrame(() => hotkeyCapture.focus());
}

function closeHotkeyModal() {
  hotkeyModal.classList.add('hidden');
  hotkeyKind = null;
  if (commitTimer) { clearTimeout(commitTimer); commitTimer = null; }
}

function resetCapture() {
  captured = { mods: new Set(), key: '' };
  hotkeyCapture.textContent = '点击此处后按键';
  hotkeyCapture.classList.remove('active');
  hideHotkeyError();
  if (commitTimer) { clearTimeout(commitTimer); commitTimer = null; }
}

function showHotkeyError(msg) {
  hotkeyError.textContent = msg;
  hotkeyError.classList.remove('hidden');
}
function hideHotkeyError() {
  hotkeyError.classList.add('hidden');
}

function formatCaptured(c) {
  if (!c.key) return '';
  const order = ['Ctrl', 'Alt', 'Shift', 'Win'];
  const mods = order.filter((m) => c.mods.has(m));
  return [...mods, c.key].join('+');
}

const KEY_NAME_MAP = {
  ' ': 'Space',
  'Spacebar': 'Space',
  'Control': 'Ctrl',
  'Meta': 'Win',
  'OS': 'Win',
};

function normalizeKeyName(e) {
  const k = e.key;
  if (KEY_NAME_MAP[k]) return KEY_NAME_MAP[k];
  // F1-F12
  if (/^F([1-9]|1[0-2])$/.test(k)) return k;
  // 单字符（字母 / 数字）
  if (k.length === 1) {
    if (/^[a-zA-Z0-9]$/.test(k)) return k.toUpperCase();
  }
  return null; // 其他键不接受
}

function isModifier(e) {
  return ['Control', 'Alt', 'Shift', 'Meta', 'OS'].includes(e.key);
}

hotkeyCapture.addEventListener('focus', () => {
  hotkeyCapture.classList.add('active');
  if (!captured.key) hotkeyCapture.textContent = '请按下你想要的快捷键';
});
hotkeyCapture.addEventListener('blur', () => {
  hotkeyCapture.classList.remove('active');
});

document.addEventListener('keydown', (e) => {
  if (hotkeyModal.classList.contains('hidden')) return;
  if (e.key === 'Escape') {
    e.preventDefault();
    closeHotkeyModal();
    return;
  }
  e.preventDefault();
  e.stopPropagation();

  // 收集修饰键状态
  const mods = new Set();
  if (e.ctrlKey) mods.add('Ctrl');
  if (e.altKey) mods.add('Alt');
  if (e.shiftKey) mods.add('Shift');
  if (e.metaKey) mods.add('Win');

  if (isModifier(e)) {
    captured.mods = mods;
    captured.key = '';
    hotkeyCapture.textContent = formatCaptured(captured) || '请按下你想要的快捷键';
    return;
  }

  const keyName = normalizeKeyName(e);
  if (!keyName) {
    showHotkeyError('该键不支持，请尝试 F1–F12 / 字母 / 数字 / Space');
    return;
  }
  hideHotkeyError();
  captured.mods = mods;
  captured.key = keyName;
  hotkeyCapture.textContent = formatCaptured(captured);

  // 防抖：300ms 内没有新键就提交
  if (commitTimer) clearTimeout(commitTimer);
  commitTimer = setTimeout(commitHotkey, 300);
}, true);

document.addEventListener('keyup', (e) => {
  if (hotkeyModal.classList.contains('hidden')) return;
  e.preventDefault();
  // 主键松开后让防抖计时继续走；不再做额外动作
}, true);

async function commitHotkey() {
  commitTimer = null;
  const spec = formatCaptured(captured);
  if (!spec || !captured.key) {
    showHotkeyError('未捕获到主键');
    return;
  }
  try {
    const kind = hotkeyKind;
    const label = HOTKEY_KIND_LABEL[kind] || '快捷键';
    await UpdateHotkey(kind, spec);
    closeHotkeyModal();
    flashStatus('notice', '✓', `${label}热键已更新为 ${spec}`, 1500);
  } catch (e) {
    showHotkeyError(String(e?.message || e || '保存失败'));
  }
}

$('hotkey-clear').addEventListener('click', () => {
  resetCapture();
  hotkeyCapture.focus();
});
$('hotkey-cancel').addEventListener('click', closeHotkeyModal);
hotkeyModal.addEventListener('click', (e) => {
  if (e.target === hotkeyModal) closeHotkeyModal();
});


// ==================== 系统提示词弹窗 ====================
const promptModal = $('prompt-modal');
const promptInput = $('prompt-input');
const promptError = $('prompt-error');
const promptHistoryList = $('prompt-history');
const promptHistoryEmpty = $('prompt-history-empty');

let promptCachedActive = '';

async function openPromptModal() {
  promptError.classList.add('hidden');
  await refreshPromptModal();
  promptModal.classList.remove('hidden');
  requestAnimationFrame(() => promptInput.focus());
}

async function refreshPromptModal() {
  try {
    const state = await GetSystemPromptState();
    promptCachedActive = state?.active || '';
    promptInput.value = promptCachedActive;
    renderPromptHistory(state?.history || []);
  } catch (e) {
    promptInput.value = '';
    renderPromptHistory([]);
  }
}

function renderPromptHistory(items) {
  promptHistoryList.innerHTML = '';
  if (!items || items.length === 0) {
    promptHistoryEmpty.classList.remove('hidden');
    return;
  }
  promptHistoryEmpty.classList.add('hidden');
  for (const text of items) {
    const row = document.createElement('div');
    row.className = 'prompt-row';
    if (text === promptCachedActive) row.classList.add('active');

    const mark = document.createElement('span');
    mark.className = 'prompt-active-mark';
    if (text === promptCachedActive) {
      mark.textContent = '●';
    } else {
      mark.classList.add('empty');
      mark.textContent = '●';
    }
    row.appendChild(mark);

    const content = document.createElement('div');
    content.className = 'prompt-row-content';
    content.textContent = text;
    row.appendChild(content);

    const del = document.createElement('button');
    del.type = 'button';
    del.className = 'prompt-row-delete';
    del.textContent = '×';
    del.title = '从历史中删除';
    del.addEventListener('click', async (e) => {
      e.stopPropagation();
      if (!confirm('从历史中删除该提示词？')) return;
      try {
        await DeleteSystemPromptHistory(text);
        await refreshPromptModal();
      } catch (err) {
        alert(String(err?.message || err));
      }
    });
    row.appendChild(del);

    row.addEventListener('click', () => {
      promptInput.value = text;
      promptInput.focus();
    });
    promptHistoryList.appendChild(row);
  }
}

function closePromptModal() {
  promptModal.classList.add('hidden');
}

$('prompt-cancel').addEventListener('click', closePromptModal);
$('prompt-save').addEventListener('click', async () => {
  promptError.classList.add('hidden');
  const content = promptInput.value;
  if (!content || !content.trim()) {
    promptError.textContent = '系统提示词不能为空';
    promptError.classList.remove('hidden');
    return;
  }
  try {
    await SaveSystemPrompt(content);
    closePromptModal();
    flashStatus('notice', '✓', '系统提示词已保存（立即生效）', 2000);
  } catch (e) {
    promptError.textContent = String(e?.message || e || '保存失败');
    promptError.classList.remove('hidden');
  }
});
promptModal.addEventListener('click', (e) => {
  if (e.target === promptModal) closePromptModal();
});

// ==================== 文档管理弹窗 ====================
const docsModal = $('docs-modal');
const docsList = $('docs-list');
const docsBadge = $('docs-badge');
const menuDocCount = $('menu-doc-count');
let cachedDocs = [];

async function refreshDocs() {
  try {
    cachedDocs = (await ListDocuments()) || [];
  } catch (_) {
    cachedDocs = [];
  }
  renderDocsList();
  refreshDocsBadge();
}

function refreshDocsBadge() {
  const enabled = cachedDocs.filter((d) => d.enabled && !d.broken);
  if (menuDocCount) menuDocCount.textContent = enabled.length;
  if (enabled.length === 0) {
    docsBadge.classList.add('hidden');
    return;
  }
  docsBadge.classList.remove('hidden');
  if (enabled.length === 1) {
    docsBadge.textContent = `📎 ${enabled[0].name}`;
  } else {
    docsBadge.textContent = `📎 ${enabled.length} 份文档`;
  }
}

function renderDocsList() {
  docsList.innerHTML = '';
  for (const d of cachedDocs) {
    const row = document.createElement('div');
    row.className = 'doc-row' + (d.broken ? ' broken' : '');

    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.className = 'doc-checkbox';
    cb.checked = d.enabled;
    cb.disabled = d.broken;
    cb.addEventListener('change', async () => {
      try {
        await SetDocumentEnabled(d.id, cb.checked);
      } catch (e) {
        cb.checked = !cb.checked;
        alert(String(e?.message || e));
      }
    });
    row.appendChild(cb);

    const info = document.createElement('div');
    info.className = 'doc-info';
    const name = document.createElement('div');
    name.className = 'doc-name';
    name.textContent = d.name;
    info.appendChild(name);
    const meta = document.createElement('div');
    meta.className = 'doc-meta';
    meta.textContent = `${d.format.toUpperCase()} · ${formatChars(d.char_count)}`;
    if (d.char_count > 100000) {
      const warn = document.createElement('span');
      warn.className = 'doc-warn';
      warn.textContent = ' ⚠ 文档较长，会增加 token 消耗';
      meta.appendChild(warn);
    }
    if (d.broken) {
      const tag = document.createElement('span');
      tag.className = 'doc-broken-tag';
      tag.textContent = ' · 文本副本丢失';
      meta.appendChild(tag);
    }
    info.appendChild(meta);
    row.appendChild(info);

    const actions = document.createElement('div');
    actions.className = 'doc-actions';
    if (!d.broken) {
      const btnPreview = document.createElement('button');
      btnPreview.className = 'doc-action';
      btnPreview.textContent = '预览';
      btnPreview.addEventListener('click', () => openPreview(d));
      actions.appendChild(btnPreview);
    }
    const btnRemove = document.createElement('button');
    btnRemove.className = 'doc-action danger';
    btnRemove.textContent = '删除';
    btnRemove.addEventListener('click', async () => {
      if (!confirm(`删除文档"${d.name}"？该操作不可撤销。`)) return;
      try {
        await RemoveDocument(d.id);
      } catch (e) {
        alert(String(e?.message || e));
      }
    });
    actions.appendChild(btnRemove);
    row.appendChild(actions);

    docsList.appendChild(row);
  }
}

function formatChars(n) {
  if (n >= 10000) return `${(n / 10000).toFixed(1)} 万字`;
  return `${n.toLocaleString()} 字`;
}

async function openDocsModal() {
  await refreshDocs();
  docsModal.classList.remove('hidden');
}

function closeDocsModal() {
  docsModal.classList.add('hidden');
}

$('docs-add').addEventListener('click', async () => {
  try {
    const doc = await AddDocument();
    if (doc) {
      flashStatus('notice', '✓', `已添加：${doc.name}`, 2000);
    }
  } catch (e) {
    alert('添加失败: ' + (e?.message || e));
  }
});

$('docs-close').addEventListener('click', closeDocsModal);
docsModal.addEventListener('click', (e) => {
  if (e.target === docsModal) closeDocsModal();
});

docsBadge.addEventListener('click', () => openDocsModal());

EventsOn('documents-changed', refreshDocs);

// 启动时拉一次
refreshDocs();

// ==================== 文档预览弹窗 ====================
const previewModal = $('preview-modal');
const previewTitle = $('preview-title');
const previewContent = $('preview-content');
const previewTruncated = $('preview-truncated');

async function openPreview(d) {
  try {
    const result = await GetDocumentPreview(d.id);
    previewTitle.textContent = `预览：${d.name}`;
    previewContent.textContent = result?.text || '';
    if (result?.truncated) {
      previewTruncated.textContent = `内容较长，仅显示前 ${formatChars(previewContent.textContent.length)}（完整内容会作为参考资料发给模型）。`;
      previewTruncated.classList.remove('hidden');
    } else {
      previewTruncated.classList.add('hidden');
    }
    previewModal.classList.remove('hidden');
  } catch (e) {
    alert('预览失败: ' + (e?.message || e));
  }
}

$('preview-close').addEventListener('click', () => previewModal.classList.add('hidden'));
previewModal.addEventListener('click', (e) => {
  if (e.target === previewModal) previewModal.classList.add('hidden');
});
