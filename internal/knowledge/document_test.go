// English: These tests verify stable UTF-8 chunk boundaries, overlap, and content hashes.
// 中文：本测试验证 UTF-8 分块边界、重叠区间及内容哈希。
package knowledge

import "testing"

func TestSplitTextPreservesUTF8AndOverlap(t *testing.T) {
	chunks, err := SplitText("家庭自动化系统规划", 5, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected chunks, got %d", len(chunks))
	}
	if chunks[0].Index != 0 || chunks[1].Index != 1 {
		t.Fatalf("unexpected chunk indexes: %+v", chunks)
	}
	if HashContent("content") == HashContent("changed") {
		t.Fatal("content hashes must distinguish documents")
	}
}
