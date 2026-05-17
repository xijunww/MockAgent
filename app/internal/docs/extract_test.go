package docs

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsSupported(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"a.txt", true}, {"b.MD", true}, {"c.markdown", true},
		{"d.docx", true}, {"e.PDF", true},
		{"f.doc", false}, {"g.exe", false}, {"h", false},
	}
	for _, c := range cases {
		if got := IsSupported(c.path); got != c.want {
			t.Errorf("IsSupported(%q)=%v want %v", c.path, got, c.want)
		}
	}
}

func TestFormat(t *testing.T) {
	if Format("简历.PDF") != "pdf" {
		t.Errorf("Format should lowercase and strip dot")
	}
	if Format("noext") != "" {
		t.Errorf("Format empty for no ext")
	}
}

func TestExtractText_NormalizesLineEndings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	// CRLF + UTF-8 BOM
	content := []byte{0xEF, 0xBB, 0xBF}
	content = append(content, []byte("a\r\nb\rc\n")...)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Extract(path)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got != "a\nb\nc\n" {
		t.Errorf("got %q want %q", got, "a\nb\nc\n")
	}
}

func TestExtract_Markdown(t *testing.T) {
	got, err := Extract("testdata/sample.md")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(got, "标题") || !strings.Contains(got, "列表项 A") {
		t.Errorf("md content lost: %q", got)
	}
}

func TestExtract_Txt(t *testing.T) {
	got, err := Extract("testdata/sample.txt")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(got, "你好世界") {
		t.Errorf("txt utf-8 lost: %q", got)
	}
}

func TestExtract_RejectsDoc(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.doc")
	os.WriteFile(path, []byte("fake binary"), 0o644)
	_, err := Extract(path)
	if err == nil || !strings.Contains(err.Error(), ".doc") {
		t.Errorf("expected friendly .doc rejection, got %v", err)
	}
}

func TestExtract_UnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.bin")
	os.WriteFile(path, []byte("xx"), 0o644)
	_, err := Extract(path)
	if err == nil {
		t.Fatal("expected unsupported error")
	}
}

func TestExtract_DOCX_MinimalValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.docx")
	if err := writeMinimalDocx(path, [][]string{
		{"第一段", "第二行"},
		{"第二段"},
	}); err != nil {
		t.Fatalf("writeMinimalDocx: %v", err)
	}
	got, err := Extract(path)
	if err != nil {
		t.Fatalf("Extract docx: %v", err)
	}
	for _, want := range []string{"第一段", "第二行", "第二段"} {
		if !strings.Contains(got, want) {
			t.Errorf("docx text missing %q in %q", want, got)
		}
	}
}

// writeMinimalDocx 写出一个最小化但合法的 docx 文件。
// runs 是段落列表，每段是若干 run（每个 run 一段连续文本）。
func writeMinimalDocx(path string, paragraphs [][]string) error {
	var bodyXML bytes.Buffer
	bodyXML.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	bodyXML.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paragraphs {
		bodyXML.WriteString(`<w:p>`)
		for i, run := range p {
			bodyXML.WriteString(`<w:r><w:t>`)
			bodyXML.WriteString(escapeXML(run))
			bodyXML.WriteString(`</w:t></w:r>`)
			if i < len(p)-1 {
				bodyXML.WriteString(`<w:r><w:br/></w:r>`)
			}
		}
		bodyXML.WriteString(`</w:p>`)
	}
	bodyXML.WriteString(`</w:body></w:document>`)

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		return err
	}
	if _, err := w.Write(bodyXML.Bytes()); err != nil {
		return err
	}
	return zw.Close()
}

func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
