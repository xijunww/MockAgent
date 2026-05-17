package docs

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrUnsupportedFormat 当后缀不在受支持列表内时返回。
var ErrUnsupportedFormat = errors.New("docs: 不支持的文件格式")

// Format 返回归一化后的格式标识（小写，无前导点），如 "pdf" / "docx" / "txt"。
func Format(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return ""
	}
	return strings.TrimPrefix(ext, ".")
}

// IsSupported 判断 path 的扩展名是否被支持。
func IsSupported(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf", ".docx":
		return true
	}
	return textExtensions[ext]
}

// Extract 把 path 的内容提取为纯文本。
// 不支持的后缀返回 ErrUnsupportedFormat（特别地，旧版 .doc 会给出明确提示）。
func Extract(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		return extractPDF(path)
	case ".docx":
		return extractDOCX(path)
	case ".doc":
		return "", fmt.Errorf("不支持 .doc 格式（旧版 Word 二进制），请另存为 .docx 后再上传")
	}
	if textExtensions[ext] {
		return extractText(path)
	}
	return "", fmt.Errorf("%w: %s", ErrUnsupportedFormat, ext)
}
