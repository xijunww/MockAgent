package docs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func TestManager_AddListGetRemove(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "resume.md")
	writeFile(t, src, "# 个人简历\n\n姓名：张三\n经历：略")

	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.List()) != 0 {
		t.Errorf("fresh manager should be empty, got %d", len(m.List()))
	}
	doc, err := m.Add(src)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if doc.Format != "md" {
		t.Errorf("format: %s", doc.Format)
	}
	if !doc.Enabled {
		t.Error("default Enabled should be true")
	}
	if doc.CharCount == 0 {
		t.Error("char count should be > 0")
	}

	got := m.List()
	if len(got) != 1 {
		t.Fatalf("List len = %d", len(got))
	}

	// 文本副本应已写入
	textPath := filepath.Join(dir, DocsDirName, doc.ID+".txt")
	if _, err := os.Stat(textPath); err != nil {
		t.Errorf("text copy not written: %v", err)
	}

	// documents.json 应已生成
	idx := filepath.Join(dir, IndexFileName)
	raw, err := os.ReadFile(idx)
	if err != nil {
		t.Fatalf("documents.json missing: %v", err)
	}
	var parsed indexFile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("documents.json parse: %v", err)
	}
	if len(parsed.Documents) != 1 || parsed.Documents[0].ID != doc.ID {
		t.Errorf("index content unexpected: %+v", parsed)
	}

	// 删除
	if err := m.Remove(doc.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(m.List()) != 0 {
		t.Error("List should be empty after Remove")
	}
	if _, err := os.Stat(textPath); !os.IsNotExist(err) {
		t.Errorf("text copy should be removed")
	}
}

func TestManager_LoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	writeFile(t, src, "hello")
	m1, _ := Load(dir)
	doc, err := m1.Add(src)
	if err != nil {
		t.Fatal(err)
	}

	m2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := m2.List()
	if len(got) != 1 || got[0].ID != doc.ID {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got[0].Broken {
		t.Error("intact doc should not be Broken")
	}
}

func TestManager_BrokenWhenTextMissing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	writeFile(t, src, "hello")
	m1, _ := Load(dir)
	doc, _ := m1.Add(src)
	// 模拟用户删了文本副本
	os.Remove(filepath.Join(dir, DocsDirName, doc.ID+".txt"))
	m2, _ := Load(dir)
	got := m2.List()
	if len(got) != 1 || !got[0].Broken {
		t.Errorf("expected Broken=true, got %+v", got)
	}
	// 启用损坏文档应失败
	if err := m2.SetEnabled(doc.ID, true); err == nil {
		t.Error("enabling broken doc should fail")
	}
}

func TestManager_SetEnabled(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	writeFile(t, src, "hello")
	m, _ := Load(dir)
	doc, _ := m.Add(src)

	if err := m.SetEnabled(doc.ID, false); err != nil {
		t.Fatal(err)
	}
	if m.List()[0].Enabled {
		t.Error("Enabled should be false")
	}
	if m.CountEnabled() != 0 {
		t.Errorf("CountEnabled=%d, want 0", m.CountEnabled())
	}
	if err := m.SetEnabled(doc.ID, true); err != nil {
		t.Fatal(err)
	}
	if m.CountEnabled() != 1 {
		t.Errorf("CountEnabled=%d, want 1", m.CountEnabled())
	}
}

func TestManager_BuildContext(t *testing.T) {
	dir := t.TempDir()
	src1 := filepath.Join(dir, "doc1.txt")
	src2 := filepath.Join(dir, "doc2.txt")
	writeFile(t, src1, "AAA")
	writeFile(t, src2, "BBB")

	m, _ := Load(dir)
	d1, _ := m.Add(src1)
	d2, _ := m.Add(src2)

	ctx := m.BuildContext()
	if !strings.Contains(ctx, "AAA") || !strings.Contains(ctx, "BBB") {
		t.Errorf("BuildContext missing content: %q", ctx)
	}
	for _, want := range []string{"--- 参考文档:", d1.Name, d2.Name} {
		if !strings.Contains(ctx, want) {
			t.Errorf("BuildContext missing %q: %q", want, ctx)
		}
	}

	// 禁用 d1 后 BuildContext 不再含 AAA
	if err := m.SetEnabled(d1.ID, false); err != nil {
		t.Fatal(err)
	}
	ctx2 := m.BuildContext()
	if strings.Contains(ctx2, "AAA") {
		t.Error("disabled doc should not appear in context")
	}
	if !strings.Contains(ctx2, "BBB") {
		t.Error("enabled doc should still appear")
	}

	// 全部禁用 → 空字符串
	m.SetEnabled(d2.ID, false)
	if m.BuildContext() != "" {
		t.Error("all disabled should yield empty context")
	}
}

func TestManager_Preview(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	writeFile(t, src, strings.Repeat("中", PreviewMaxRunes+500))
	m, _ := Load(dir)
	doc, _ := m.Add(src)
	preview, truncated, err := m.Preview(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Error("expected truncated=true")
	}
	if len([]rune(preview)) != PreviewMaxRunes {
		t.Errorf("preview rune count %d != %d", len([]rune(preview)), PreviewMaxRunes)
	}
}

func TestManager_AddRejectsUnsupported(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.exe")
	writeFile(t, src, "x")
	m, _ := Load(dir)
	_, err := m.Add(src)
	if err == nil {
		t.Error("expected unsupported error")
	}
}

func TestManager_RemoveUnknownID(t *testing.T) {
	dir := t.TempDir()
	m, _ := Load(dir)
	if err := m.Remove("nope"); err == nil {
		t.Error("Remove of unknown id should fail")
	}
}

func TestManager_LoadCorruptIndex(t *testing.T) {
	dir := t.TempDir()
	// 写一份损坏的 documents.json
	idxPath := filepath.Join(dir, IndexFileName)
	writeFile(t, idxPath, "{corrupt")
	m, err := Load(dir)
	if err == nil {
		t.Error("should return error for corrupt json")
	}
	if m == nil {
		t.Fatal("manager should still be returned")
	}
	if len(m.List()) != 0 {
		t.Error("should start empty after corruption")
	}
	if _, err := os.Stat(idxPath + ".bak"); err != nil {
		t.Errorf("corrupt file should be backed up to .bak: %v", err)
	}
}
