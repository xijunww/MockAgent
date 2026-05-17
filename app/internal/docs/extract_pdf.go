package docs

import (
	"bytes"
	"fmt"

	"github.com/ledongthuc/pdf"
)

// extractPDF 从 PDF 文件中提取纯文本。
// 仅支持文本型 PDF；扫描版（图片）会返回空字符串，由调用方决定如何提示。
func extractPDF(path string) (string, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开 PDF 失败: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	totalPages := r.NumPage()
	for i := 1; i <= totalPages; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			// 某些页面可能编码异常；跳过单页继续，避免一坏全废
			continue
		}
		buf.WriteString(text)
		buf.WriteString("\n")
	}
	cleaned, _ := cleanText(buf.Bytes())
	return cleaned, nil
}
