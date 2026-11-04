// English: These tests validate Goal Job record invariants and Supervisor outcome mapping.
// 中文：本测试验证 Goal Job 记录不变量和 Supervisor 结果到 Job 状态映射。
package supervisor

import (
	"testing"
	"time"

	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/goal"
)

func TestGoalJobRequiresLeaseOnlyWhileRunning(t *testing.T) {
	base := GoalJob{ID: "job-1", GoalID: "goal-1", Status: JobPending, MaxRuns: 10, FencingToken: 0}
	if err := base.Validate(); err != nil {
		t.Fatalf("pending Job invalid: %v", err)
	}
	base.Status = JobRunning
	if err := base.Validate(); err == nil {
		t.Fatal("running Job without lease was accepted")
	}
	base.LeaseOwner, base.LeaseExpires, base.HeartbeatAt, base.FencingToken = "worker", time.Now().Add(time.Second), time.Now(), 1
	if err := base.Validate(); err != nil {
		t.Fatalf("leased running Job invalid: %v", err)
	}
}

func TestGoalJobMapsSupervisorWaitAndTerminalResults(t *testing.T) {
	cases := []struct {
		status     string
		goalStatus goal.Status
		want       JobStatus
	}{
		{status: string(execution.StatusWaitingApproval), want: JobWaitingApproval},
		{status: string(execution.StatusWaitingExternal), want: JobWaitingExternal},
		{status: string(execution.StatusUnknown), want: JobWaitingExternal},
		{goalStatus: goal.Completed, want: JobCompleted},
		{goalStatus: goal.Cancelled, want: JobCancelled},
		{status: "budget_exhausted", want: JobFailed},
		{status: "awaiting_verification", want: JobWaitingExternal},
		{goalStatus: goal.Active, want: JobPending},
	}
	for _, tc := range cases {
		got := jobStatusForTurn(TurnResult{Status: tc.status, Goal: goal.Goal{Status: tc.goalStatus}})
		if got != tc.want {
			t.Errorf("jobStatusForTurn(%q,%q)=%q want %q", tc.status, tc.goalStatus, got, tc.want)
		}
	}
}
