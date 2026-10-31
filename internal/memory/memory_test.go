// English: These tests ensure memories require provenance and supported classifications.
// 中文：本测试确保记忆必须包含来源信息且使用支持的分类。
// 中文：本测试确保记忆必须提供来源并使用受支持的分类。
package memory

import "testing"

func TestMemoryRequiresProvenanceAndValidCategory(t *testing.T) {
	item := Memory{Category: Fact, Content: "Uses local-first software", Source: "explicit user statement", Confidence: 0.9}
	if err := item.Validate(); err != nil {
		t.Fatalf("valid memory rejected: %v", err)
	}
	item.Source = ""
	if err := item.Validate(); err == nil {
		t.Fatal("memory without source must be rejected")
	}
	item.Source = "conversation"
	item.Category = "chat-dump"
	if err := item.Validate(); err == nil {
		t.Fatal("unknown category must be rejected")
	}
}
