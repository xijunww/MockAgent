package docs

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// extractDOCX 解析 .docx（OOXML）容器，从 word/document.xml 中按 w:t 顺序拼接文本。
//
// 用最小化的 zip+xml 实现，不引入第三方库。
func extractDOCX(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("打开 docx 失败: %w", err)
	}
	defer zr.Close()

	var docFile *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docFile = f
			break
		}
	}
	if docFile == nil {
		return "", fmt.Errorf("docx 中未找到 word/document.xml")
	}

	rc, err := docFile.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	return parseDocxBody(rc)
}

// parseDocxBody 从 document.xml 流式解析，按段落拼接：
//   - <w:t>: 收集为当前段落文本
//   - <w:tab/>: 制表符
//   - <w:br/>: 软换行
//   - </w:p>: 段落结束 → 写入一行
func parseDocxBody(r io.Reader) (string, error) {
	dec := xml.NewDecoder(r)
	var (
		out          strings.Builder
		paragraph    strings.Builder
		insideText   bool
	)

	flushParagraph := func() {
		out.WriteString(paragraph.String())
		out.WriteString("\n")
		paragraph.Reset()
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("解析 docx XML 失败: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				insideText = true
			case "tab":
				paragraph.WriteRune('\t')
			case "br":
				paragraph.WriteRune('\n')
			}
		case xml.CharData:
			if insideText {
				paragraph.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				insideText = false
			case "p":
				flushParagraph()
			}
		}
	}

	// 文档可能不以 </w:p> 结尾；把残余刷一次
	if paragraph.Len() > 0 {
		flushParagraph()
	}
	cleaned, _ := cleanText([]byte(out.String()))
	return cleaned, nil
}
