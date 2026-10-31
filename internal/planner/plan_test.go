// English: These tests cover deterministic plan output and dependency graph rejection.
// 中文：本测试覆盖确定性计划输出和无效任务依赖图的拒绝规则。
// 中文：本测试覆盖确定性计划输出及无效依赖图拒绝规则。
package planner

import (
	"context"
	"testing"
)

func TestMockPlannerReturnsValidatedDependencyPlan(t *testing.T) {
	plan, err := (MockGenerator{}).GenerateContext(context.Background(), GoalInput{ID: "goal-1", Title: "Home automation"})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("invalid plan: %v", err)
	}
	if len(plan.Tasks) < 2 || plan.EstimatedHours <= 0 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestPlanRejectsCyclesAndMissingDependencies(t *testing.T) {
	plan := Plan{GoalID: "goal", Summary: "test", Tasks: []TaskSpec{{Key: "a", Title: "A", DependsOn: []string{"b"}}, {Key: "b", Title: "B", DependsOn: []string{"a"}}}}
	if err := plan.Validate(); err == nil {
		t.Fatal("expected cycle rejection")
	}
	plan.Tasks = []TaskSpec{{Key: "a", Title: "A", DependsOn: []string{"missing"}}}
	if err := plan.Validate(); err == nil {
		t.Fatal("expected missing dependency rejection")
	}
}
