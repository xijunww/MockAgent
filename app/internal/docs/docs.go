// Package docs 管理 LLM 的参考文档：上传、提取、启用/禁用、拼接到 system prompt。
//
// 持久化布局（与 config.json 同目录）：
//
//	<dir>/documents.json    元数据数组
//	<dir>/documents/        提取后的纯文本副本，文件名为 <id>.txt
package docs

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"
)

const (
	// IndexFileName 元数据文件名。
	IndexFileName = "documents.json"
	// DocsDirName 文本副本子目录。
	DocsDirName = "documents"
	// LongDocumentRunes 超过此字符数视为长文档（UI 显示警告）。
	LongDocumentRunes = 100_000
	// PreviewMaxRunes 预览时返回的最大字符数。
	PreviewMaxRunes = 10_000
	// IndexVersion 当前 documents.json schema 版本。
	IndexVersion = 1
)

// Document 是文档元数据。文本本身存在 <id>.txt 文件中，不进 JSON。
type Document struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`        // 显示名（默认取原文件 Base 名）
	SourcePath string    `json:"source_path"` // 上传时的原路径，仅供参考
	Format     string    `json:"format"`      // "pdf" / "docx" / "txt" 等
	CharCount  int       `json:"char_count"`  // utf8 rune 数
	Enabled    bool      `json:"enabled"`
	AddedAt    time.Time `json:"added_at"`
	// Broken 在加载时检测到 <id>.txt 缺失或异常；不参与 JSON 序列化（每次启动重算）。
	Broken bool `json:"-"`
}

// IsLong 当字符数超过 LongDocumentRunes 时返回 true。
func (d Document) IsLong() bool { return d.CharCount > LongDocumentRunes }

// indexFile 是 documents.json 的根结构。
type indexFile struct {
	Version   int        `json:"version"`
	Documents []Document `json:"documents"`
}

// Manager 在内存中维护文档列表并负责持久化。线程安全。
type Manager struct {
	mu  sync.RWMutex
	dir string
	// docs 元数据；切片顺序就是 UI 显示顺序（按添加时间）。
	docs []Document

	// 用于生成 ULID 的随机源
	entropy *ulid.MonotonicEntropy
}

// New 创建 Manager 但不会读取磁盘；调用方一般用 Load。
func New(dir string) *Manager {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	return &Manager{
		dir:     dir,
		entropy: ulid.Monotonic(rng, 0),
	}
}

// Load 从 dir 读取已保存的文档元数据；不存在时返回空 Manager。
//
// 损坏的 documents.json 会被备份成 .bak，然后从空列表开始；
// 缺失的 <id>.txt 文件会让对应文档被标记为 Broken。
func Load(dir string) (*Manager, error) {
	m := New(dir)
	if err := os.MkdirAll(filepath.Join(dir, DocsDirName), 0o755); err != nil {
		return nil, fmt.Errorf("创建 documents 目录失败: %w", err)
	}
	indexPath := filepath.Join(dir, IndexFileName)
	raw, err := os.ReadFile(indexPath)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", IndexFileName, err)
	}

	var idx indexFile
	if err := json.Unmarshal(raw, &idx); err != nil {
		// 备份损坏的文件继续启动
		_ = os.Rename(indexPath, indexPath+".bak")
		return m, fmt.Errorf("%s 解析失败，已备份为 .bak: %w", IndexFileName, err)
	}

	for _, d := range idx.Documents {
		// 验证文本副本是否还在
		path := m.textPath(d.ID)
		if _, err := os.Stat(path); err != nil {
			d.Broken = true
		}
		m.docs = append(m.docs, d)
	}
	sort.SliceStable(m.docs, func(i, j int) bool {
		return m.docs[i].AddedAt.Before(m.docs[j].AddedAt)
	})
	return m, nil
}

// List 返回当前文档元数据的拷贝。
func (m *Manager) List() []Document {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Document, len(m.docs))
	copy(out, m.docs)
	return out
}

// CountEnabled 返回当前启用且未损坏的文档数量。
func (m *Manager) CountEnabled() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, d := range m.docs {
		if d.Enabled && !d.Broken {
			n++
		}
	}
	return n
}

// Add 把 path 处的文件解析后加入文档列表。
//
// 流程：
//  1. 校验后缀名
//  2. 提取文本
//  3. 写到 <id>.txt
//  4. 把元数据 append 到内存并持久化 documents.json
func (m *Manager) Add(path string) (Document, error) {
	if !IsSupported(path) {
		return Document{}, fmt.Errorf("%w: %s", ErrUnsupportedFormat, filepath.Ext(path))
	}
	text, err := Extract(path)
	if err != nil {
		return Document{}, err
	}

	m.mu.Lock()
	id := ulid.MustNew(ulid.Timestamp(time.Now()), m.entropy).String()
	m.mu.Unlock()

	if err := os.MkdirAll(filepath.Join(m.dir, DocsDirName), 0o755); err != nil {
		return Document{}, fmt.Errorf("创建 documents 目录失败: %w", err)
	}
	if err := os.WriteFile(m.textPath(id), []byte(text), 0o644); err != nil {
		return Document{}, fmt.Errorf("写入文本副本失败: %w", err)
	}

	doc := Document{
		ID:         id,
		Name:       filepath.Base(path),
		SourcePath: path,
		Format:     Format(path),
		CharCount:  utf8.RuneCountInString(text),
		Enabled:    true,
		AddedAt:    time.Now(),
	}

	m.mu.Lock()
	m.docs = append(m.docs, doc)
	saveErr := m.saveLocked()
	m.mu.Unlock()
	if saveErr != nil {
		// 写元数据失败：清理已写入的文本文件
		_ = os.Remove(m.textPath(id))
		// 同时回滚内存
		m.mu.Lock()
		if len(m.docs) > 0 && m.docs[len(m.docs)-1].ID == id {
			m.docs = m.docs[:len(m.docs)-1]
		}
		m.mu.Unlock()
		return Document{}, saveErr
	}
	return doc, nil
}

// Remove 删除指定 id 的文档（含磁盘上的 <id>.txt）。
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.findIndexLocked(id)
	if idx < 0 {
		return fmt.Errorf("docs: 未找到 id=%s", id)
	}
	m.docs = append(m.docs[:idx], m.docs[idx+1:]...)
	if err := m.saveLocked(); err != nil {
		return err
	}
	_ = os.Remove(m.textPath(id))
	return nil
}

// SetEnabled 切换启用状态。损坏的文档不能被启用。
func (m *Manager) SetEnabled(id string, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.findIndexLocked(id)
	if idx < 0 {
		return fmt.Errorf("docs: 未找到 id=%s", id)
	}
	if on && m.docs[idx].Broken {
		return errors.New("文档已损坏，无法启用；请重新上传")
	}
	m.docs[idx].Enabled = on
	return m.saveLocked()
}

// GetText 读取文档的提取文本，返回原文（可能很长）。
func (m *Manager) GetText(id string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.findIndexLocked(id) < 0 {
		return "", fmt.Errorf("docs: 未找到 id=%s", id)
	}
	raw, err := os.ReadFile(m.textPath(id))
	if err != nil {
		return "", fmt.Errorf("读取文本副本失败: %w", err)
	}
	return string(raw), nil
}

// Preview 返回文档的预览（截断到 PreviewMaxRunes 字符）。
//
// 第二个返回值表示是否被截断。
func (m *Manager) Preview(id string) (string, bool, error) {
	full, err := m.GetText(id)
	if err != nil {
		return "", false, err
	}
	if utf8.RuneCountInString(full) <= PreviewMaxRunes {
		return full, false, nil
	}
	// 按 rune 截断，避免在多字节中间切断
	runes := []rune(full)
	return string(runes[:PreviewMaxRunes]), true, nil
}

// BuildContext 把所有启用且未损坏的文档拼接成一段文本，用作 system prompt 的扩展。
//
// 输出格式（多份文档之间空行分隔）：
//
//	--- 参考文档: <name> ---
//	<文档正文>
//
// 没有任何启用文档时返回空字符串。
func (m *Manager) BuildContext() string {
	m.mu.RLock()
	enabled := make([]Document, 0, len(m.docs))
	for _, d := range m.docs {
		if d.Enabled && !d.Broken {
			enabled = append(enabled, d)
		}
	}
	m.mu.RUnlock()

	if len(enabled) == 0 {
		return ""
	}

	var b strings.Builder
	for i, d := range enabled {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "--- 参考文档: %s ---\n", d.Name)
		text, err := os.ReadFile(m.textPath(d.ID))
		if err != nil {
			// 不致命：跳过本份文档但保留其他
			fmt.Fprintf(&b, "（读取失败: %v）\n", err)
			continue
		}
		b.Write(text)
	}
	return b.String()
}

// ---- 内部 ----

func (m *Manager) textPath(id string) string {
	return filepath.Join(m.dir, DocsDirName, id+".txt")
}

func (m *Manager) findIndexLocked(id string) int {
	for i, d := range m.docs {
		if d.ID == id {
			return i
		}
	}
	return -1
}

func (m *Manager) saveLocked() error {
	idx := indexFile{Version: IndexVersion, Documents: m.docs}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化文档索引失败: %w", err)
	}
	data = append(data, '\n')
	indexPath := filepath.Join(m.dir, IndexFileName)
	tmp := indexPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入 %s 临时文件失败: %w", IndexFileName, err)
	}
	if err := os.Rename(tmp, indexPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换 %s 失败: %w", IndexFileName, err)
	}
	return nil
}
