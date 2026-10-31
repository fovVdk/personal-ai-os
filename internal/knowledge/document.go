// Package knowledge provides content-addressed document ingestion and citation models.
// 中文：knowledge 包定义内容寻址文档导入和引用模型。
// English: This file models content-addressed documents, citations, and Unicode-safe chunks.
// 中文：本文件定义内容寻址文档、引用以及支持 Unicode 的文本分块结构。
package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

func (d Document) Validate() error {
	if strings.TrimSpace(d.OwnerID) == "" || strings.TrimSpace(d.Title) == "" || strings.TrimSpace(d.Source) == "" {
		return errors.New("document owner, title, and source are required")
	}
	if len(d.ContentHash) != 64 {
		return errors.New("document content hash must be SHA-256")
	}
	if len(d.Chunks) == 0 {
		return errors.New("document must contain at least one chunk")
	}
	for i, chunk := range d.Chunks {
		if chunk.Index != i || strings.TrimSpace(chunk.Content) == "" {
			return errors.New("document chunks must be non-empty and sequentially indexed")
		}
	}
	return nil
}

type Chunk struct {
	Index    int            `json:"index"`
	Content  string         `json:"content"`
	Metadata map[string]any `json:"metadata"`
}

type Document struct {
	ID          string         `json:"id"`
	OwnerID     string         `json:"owner_id"`
	Title       string         `json:"title"`
	Source      string         `json:"source"`
	ContentHash string         `json:"content_hash"`
	Metadata    map[string]any `json:"metadata"`
	Chunks      []Chunk        `json:"chunks"`
}

type Citation struct {
	DocumentID string `json:"document_id"`
	ChunkIndex int    `json:"chunk_index"`
	Title      string `json:"title"`
	Source     string `json:"source"`
	Excerpt    string `json:"excerpt"`
}

func HashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// SplitText creates deterministic rune-aware chunks and avoids splitting UTF-8 code points.
// 中文：SplitText 按 Unicode 字符确定性分块，避免切断 UTF-8 多字节字符。
func SplitText(content string, maxRunes, overlap int) ([]Chunk, error) {
	if maxRunes < 1 || overlap < 0 || overlap >= maxRunes {
		return nil, errors.New("chunk size must be positive and overlap smaller than chunk size")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, errors.New("document content is required")
	}
	runes := []rune(content)
	chunks := make([]Chunk, 0, (len(runes)+maxRunes-1)/maxRunes)
	step := maxRunes - overlap
	for start, index := 0, 0; start < len(runes); start, index = start+step, index+1 {
		end := start + maxRunes
		if end > len(runes) {
			end = len(runes)
		}
		text := strings.TrimSpace(string(runes[start:end]))
		if text != "" {
			chunks = append(chunks, Chunk{Index: index, Content: text, Metadata: map[string]any{}})
		}
		if end == len(runes) {
			break
		}
	}
	return chunks, nil
}
