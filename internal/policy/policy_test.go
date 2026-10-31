// English: These tests verify autonomy-level decisions and default-deny behavior.
// 中文：本测试验证自主等级决策及默认拒绝行为。
package policy

import "testing"

func TestEvaluateAutonomyRules(t *testing.T) {
	tests := []struct {
		name    string
		request Request
		want    Decision
	}{
		{name: "A0 allowed", request: Request{ToolID: "search", Action: "query", Level: A0, ToolAllowed: true}, want: Allow},
		{name: "A1 allowed", request: Request{ToolID: "note", Action: "create", Level: A1, ToolAllowed: true}, want: Allow},
		{name: "A2 requires configured approval", request: Request{ToolID: "message", Action: "send", Level: A2, ToolAllowed: true, ApprovalRequired: true}, want: RequireApproval},
		{name: "A3 always requires approval", request: Request{ToolID: "door", Action: "unlock", Level: A3, ToolAllowed: true}, want: RequireApproval},
		{name: "not allowlisted denied", request: Request{ToolID: "shell", Action: "run", Level: A0}, want: Deny},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Evaluate(test.request)
			if err != nil {
				t.Fatal(err)
			}
			if got.Decision != test.want {
				t.Fatalf("got %s, want %s", got.Decision, test.want)
			}
		})
	}
}
