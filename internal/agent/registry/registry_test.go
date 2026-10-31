// English: These tests cover duplicate registration and defensive profile copies.
// 中文：本测试覆盖重复注册拒绝和 Profile 防御性拷贝。
// 中文：本测试覆盖重复注册拒绝以及 Agent 配置副本隔离。
package registry

import (
	"context"
	"testing"
	"time"

	"personal-ai-os/internal/agent/runtime"
)

func TestRegistryRejectsDuplicatesAndReturnsCopies(t *testing.T) {
	r := New()
	profile := Profile{ID: "planning", Name: "Planning Agent", Version: "1", Capabilities: []string{"plan"}, Agent: runtime.MockAgent{AgentID: "planning"}}
	if err := r.Register(profile); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(profile); err == nil {
		t.Fatal("duplicate agent accepted")
	}
	loaded, err := r.Get("planning")
	if err != nil {
		t.Fatal(err)
	}
	loaded.Capabilities[0] = "changed"
	reloaded, _ := r.Get("planning")
	if reloaded.Capabilities[0] != "plan" {
		t.Fatal("registry capability slice was mutated through returned profile")
	}
	result, err := reloaded.Agent.Execute(context.Background(), runtime.Request{Title: "goal", MaxSteps: 1, MaxModelCalls: 1, MaxToolCalls: 1, MaxCost: 0, Deadline: time.Now().Add(time.Second)})
	if err != nil || result.Steps != 1 {
		t.Fatalf("mock agent result=%+v err=%v", result, err)
	}
}
