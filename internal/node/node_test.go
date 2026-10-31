// English: These tests validate supported node types and required identity fields.
// 中文：本测试验证节点类型白名单和必填身份字段。
package node

import "testing"

func TestNodeValidation(t *testing.T) {
	item := Node{Identity: "mini-core", Name: "Mac mini", Type: Core, Status: Offline, Capabilities: []string{"api", "planner"}}
	if err := item.Validate(); err != nil {
		t.Fatal(err)
	}
	item.Type = "unknown"
	if err := item.Validate(); err == nil {
		t.Fatal("unknown node type must fail")
	}
}
