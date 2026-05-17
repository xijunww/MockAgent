package docs

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// 支持的"纯文本类"后缀。匹配时统一小写。
var textExtensions = map[string]bool{
	".txt": true, ".md": true, ".markdown": true,
	".csv": true, ".tsv": true,
	".json": true, ".yaml": true, ".yml": true, ".toml": true, ".ini": true,
	".xml": true, ".html": true, ".htm": true,
	".log": true,
	".go": true, ".py": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true,
	".java": true, ".c": true, ".h": true, ".cpp": true, ".hpp": true,
	".cs": true, ".rs": true, ".rb": true, ".php": true,
	".sh": true, ".bat": true, ".ps1": true, ".sql": true,
	".css": true, ".scss": true, ".less": true,
}

func extractText(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return cleanText(raw)
}

// cleanText 规范化文本：
//   - 剥离 UTF-8 BOM
//   - 把 CRLF / 单独 CR 全部替换成 LF
//   - 校验是否合法 UTF-8
func cleanText(raw []byte) (string, error) {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(raw) {
		return "", errors.New("文件不是有效的 UTF-8 编码")
	}
	s := string(raw)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s, nil
}
