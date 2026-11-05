// English: This PostgreSQL acceptance drives a deterministic research Goal through its durable Job, Gateway, Approval, and verifier lifecycle.
// 中文：本 PostgreSQL 验收通过持久 Goal Job、Gateway、审批及 Verifier 驱动确定性研究目标。
package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/agent/registry"
	"personal-ai-os/internal/api"
	"personal-ai-os/internal/app/store"
	"personal-ai-os/internal/approval"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/planner"
	"personal-ai-os/internal/policy"
	"personal-ai-os/internal/source"
	"personal-ai-os/internal/supervisor"
	"personal-ai-os/internal/task"
	"personal-ai-os/internal/tool/builtin"
	toolgateway "personal-ai-os/internal/tool/gateway"
	toolregistry "personal-ai-os/internal/tool/registry"
	"personal-ai-os/internal/verification"
	"personal-ai-os/migrations"
)

func TestAutonomousResearchGoalE2E(t *testing.T) {
	pool, repositories := integrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	const sourceText = "The open robotics platform uses a modular controller and supports autonomous navigation in mapped indoor environments."
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Research-Source", "deterministic-integration-fixture")
		_, _ = io.WriteString(w, sourceText)
	}))
	defer server.Close()
	if parsed, err := url.Parse(server.URL); err != nil || parsed.Hostname() != "127.0.0.1" {
		t.Fatalf("expected controlled loopback HTTP fixture, URL=%q err=%v", server.URL, err)
	}

	artifactRoot := t.TempDir()
	exportRoot := t.TempDir()
	verifier, err := verification.NewArtifactVerifier(artifactRoot, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	verifier.Sources = repositories.Sources()
	repositories.SetArtifactFreshnessVerifier(verifier)

	criterion := goal.SuccessCriterion{
		ID: "research-report", Name: "Research report with verifiable source citation",
		Description: "A structured report whose citation is bound to an immutable Source Snapshot.",
		Type: goal.CriterionMarkdownStructure, Required: true,
		ValidationMethod: goal.CriterionMethodArtifactInStorage,
		ArtifactPath: "reports/research.md", RequiredSections: []string{"Summary", "Sources"},
		MaxArtifactBytes: 2 << 20, RequireReference: true,
		Source: goal.CriterionSourceUser, Confirmed: true, Version: 1,
	}
	goalItem, err := repositories.Create(ctx, goal.Goal{
		OwnerID: testOwnerID, Title: "Research an open robotics platform",
		Description: "Collect one operator-selected source, quote it, and produce a structured research artifact.",
		Priority: 4, Status: goal.Active, Criteria: []goal.SuccessCriterion{criterion},
		Constraints: []string{"Source reads use only the operator-selected fixture; report export requires exact human approval."},
	})
	if err != nil {
		t.Fatalf("create research Goal: %v", err)
	}
	goalIDs := []string{goalItem.ID}
	var taskIDs, approvalIDs []string
	t.Cleanup(func() { cleanupIntegrationRows(t, context.Background(), pool, goalIDs, taskIDs, nil, nil, nil, approvalIDs) })

	tools := toolregistry.New()
	sourceHandler := integrationSourceCapture{ownerID: testOwnerID, onlyURL: server.URL, httpClient: server.Client(), repository: repositories.Sources()}
	if err := builtin.Register(tools, builtin.Options{
		FileRoot: artifactRoot, ReportExportRoot: exportRoot, OwnerID: testOwnerID, Goals: repositories, Tasks: repositories.Tasks(),
		Sources: repositories.Sources(), HTTPFetchAllowedHosts: []string{"research.example"},
	}); err != nil {
		t.Fatalf("register bounded Goal Tools: %v", err)
	}
	if err := tools.ReplaceHandler(toolregistry.Identity{ProviderID: "paios.native", ToolID: "source.capture", Version: "v1"}, sourceHandler); err != nil {
		t.Fatalf("inject deterministic local Source HTTP handler: %v", err)
	}
	grants := integrationGrants{
		"paios.native/source.capture@v1":       {Allowed: true, Autonomy: policy.A1},
		"paios.native/source.reference.add@v1": {Allowed: true, Autonomy: policy.A1},
		"paios.native/artifact.write@v1":       {Allowed: true, Autonomy: policy.A1},
		"paios.native/report.export@v1":        {Allowed: true, Autonomy: policy.A2, ApprovalRequired: true},
	}
	durable := store.NewDurableExecutionStore(pool)
	gateway := &toolgateway.Gateway{Registry: tools, Authorizer: toolgateway.PolicyAuthorizer{Grants: grants}, Leases: repositories, Store: repositories}
	approvalAPI := httptest.NewServer((&api.Server{Goals: repositories, Tasks: repositories.Tasks(), Approvals: repositories.Approvals(), DurableExecutions: durable, DB: pool}).Handler())
	defer approvalAPI.Close()

	agents := registry.New()
	if err := agents.Register(registry.Profile{ID: "research", Name: "Deterministic Research Pipeline", Version: "test-v1", Capabilities: []string{"allowlisted-source-capture", "citation-binding", "criterion-scoped-report"}, Agent: integrationResearchAgent{sourceURL: server.URL}}); err != nil {
		t.Fatalf("register deterministic research Agent: %v", err)
	}
	planGenerator := integrationResearchPlanner{sourceURL: server.URL, criterionID: criterion.ID}
	supervisorService := &supervisor.Supervisor{
		Goals: repositories, Tasks: repositories.Tasks(), Planner: planGenerator, Agents: agents, Verifier: verifier,
		Executions: store.NewExecutionStore(pool), Evaluations: store.NewEvaluationStore(pool),
		Decisions: store.NewDecisionStore(pool), Plans: store.NewVersionedPlanStore(pool),
		Usage: store.NewModelUsageStore(pool), Recovery: repositories, Runtime: durable,
		Budgets: supervisor.Budgets{MaxSteps: 4, MaxModelCalls: 1, MaxToolCalls: 8, MaxModelTokens: 1024, MaxRetries: 0, MaxRuntime: 30 * time.Second},
	}
	jobStore := store.NewGoalJobStore(pool)
	job, err := jobStore.CreateOrWake(ctx, goalItem.ID, 10)
	if err != nil || job.Status != supervisor.JobPending {
		t.Fatalf("persist autonomous Goal Job=%+v err=%v", job, err)
	}
	workerPool, err := execution.NewWorkerPool(durable, integrationResearchExecutor{agents: agents, gateway: gateway, verifier: verifier}, execution.WorkerConfig{
		WorkerID: uniqueLabel("research-e2e-worker"), Concurrency: 1, LeaseDuration: 5 * time.Second, ExecutionTimeout: 25 * time.Second,
		HeartbeatInterval: time.Second, PollInterval: 10 * time.Millisecond, RetryBaseDelay: 20 * time.Millisecond,
		RecoveryInterval: 25 * time.Millisecond, RecoveryBatch: 20, ShutdownGrace: 2 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("create real durable Execution Worker Pool: %v", err)
	}
	dispatcher := store.NewOutboxDispatcherStore(pool)
	workerCtx, stopWorkers := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- workerPool.Run(workerCtx) }()
	dispatcherService := &eventDispatcherFixture{repository: dispatcher}
	dispatchDone := make(chan struct{})
	go func() { defer close(dispatchDone); dispatcherService.run(workerCtx) }()
	jobWorker := &supervisor.JobWorker{
		Jobs: jobStore, Goals: repositories, Supervisor: supervisorService,
		WorkerID: uniqueLabel("research-e2e-job"), Concurrency: 1, LeaseDuration: 5 * time.Second,
		HeartbeatEvery: time.Second, PollInterval: 20 * time.Millisecond, RetryBase: 25 * time.Millisecond,
		MaxRetries: 0, MaxRuns: 10,
	}
	jobDone := make(chan error, 1)
	go func() { jobDone <- jobWorker.Run(workerCtx) }()
	defer func() {
		stopWorkers()
		select {
		case <-workerDone:
		case <-time.After(3 * time.Second):
			t.Error("Execution Worker Pool did not stop")
		}
		select {
		case <-jobDone:
		case <-time.After(3 * time.Second):
			t.Error("Goal Job Worker did not stop")
		}
		<-dispatchDone
	}()

	approvalSubmitted := false
	approvedParameterHash := ""
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := jobStore.Get(ctx, testOwnerID, job.ID)
		if err != nil {
			t.Fatalf("poll durable Goal Job: %v", err)
		}
		if current.Status == supervisor.JobCompleted {
			break
		}
		if current.Status == supervisor.JobFailed || current.Status == supervisor.JobCancelled {
			t.Fatalf("autonomous Goal Job reached terminal status: %+v", current)
		}
		if current.Status == supervisor.JobWaitingApproval && !approvalSubmitted {
			pending, listErr := repositories.Approvals().ListPending(ctx, testOwnerID, 20)
			if listErr != nil || len(pending) != 1 || pending[0].Action != "report.export" || pending[0].ToolID != "paios.native/report.export" || pending[0].RiskLevel != 2 {
				t.Fatalf("research Approval request is not the exact report-export action: approvals=%+v err=%v", pending, listErr)
			}
			runCount := current.RunCount
			idleTimer := time.NewTimer(120 * time.Millisecond)
			<-idleTimer.C
			idleTimer.Stop()
			idle, err := jobStore.Get(ctx, testOwnerID, job.ID)
			if err != nil || idle.Status != supervisor.JobWaitingApproval || idle.RunCount != runCount {
				t.Fatalf("waiting Approval caused a busy Goal Job loop: before=%+v after=%+v err=%v", current, idle, err)
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, approvalAPI.URL+"/api/v1/approvals/"+pending[0].ID+"/approve", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("approve exact report export through API: %v", err)
			}
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil || response.StatusCode != http.StatusOK {
				t.Fatalf("approve report export status=%d body=%s err=%v", response.StatusCode, body, readErr)
			}
			var approved approval.Request
			if err := json.Unmarshal(body, &approved); err != nil || approved.Status != approval.Approved || approved.ParametersHash != pending[0].ParametersHash {
				t.Fatalf("Approval continuation changed approved parameters: %+v err=%v", approved, err)
			}
			approvedParameterHash = approved.ParametersHash
			approvalIDs = append(approvalIDs, approved.ID)
			approvalSubmitted = true
			continue
		}
		if current.Status == supervisor.JobWaitingExternal {
			evaluation, evalErr := supervisorService.EvaluateGoal(ctx, goalItem.ID)
			t.Fatalf("autonomous Goal Job is waiting_external: job=%+v evaluation=%+v evalErr=%v", current, evaluation, evalErr)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("autonomous research Goal did not finish: job=%+v err=%v", current, ctx.Err())
		case <-ticker.C:
		}
	}

	finalGoal, err := repositories.Get(ctx, testOwnerID, goalItem.ID)
	if err != nil || finalGoal.Status != goal.Completed {
		t.Fatalf("autonomous Goal final state=%+v err=%v", finalGoal, err)
	}
	items, err := repositories.Tasks().ListByGoal(ctx, goalItem.ID, 20)
	if err != nil || len(items) != 1 || items[0].Status != task.Completed || len(items[0].CriterionIDs) != 1 || items[0].CriterionIDs[0] != criterion.ID {
		t.Fatalf("persisted research Task state=%+v err=%v", items, err)
	}
	taskIDs = append(taskIDs, items[0].ID)
	var snapshotID string
	if err := pool.QueryRow(ctx, "SELECT id::text FROM source_snapshots WHERE goal_id=$1", goalItem.ID).Scan(&snapshotID); err != nil {
		t.Fatalf("find captured Source Snapshot: %v", err)
	}
	var sourceOriginalURL, sourceFinalURL, sourceHash string
	var sourceStatus int
	var sourceRetrievedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT original_url,final_url,http_status,retrieved_at,content_hash FROM source_snapshots WHERE id=$1`, snapshotID).Scan(&sourceOriginalURL, &sourceFinalURL, &sourceStatus, &sourceRetrievedAt, &sourceHash); err != nil {
		t.Fatalf("load Source Snapshot provenance: %v", err)
	}
	if sourceOriginalURL != server.URL || sourceFinalURL != server.URL || sourceStatus != http.StatusOK || sourceRetrievedAt.IsZero() || sourceHash == "" {
		t.Fatalf("Source Snapshot provenance/hash invalid: original=%s final=%s status=%d retrieved=%s hash=%s", sourceOriginalURL, sourceFinalURL, sourceStatus, sourceRetrievedAt, sourceHash)
	}
	var referenceCount, verificationCount, evidenceCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM source_references WHERE goal_id=$1 AND snapshot_id=$2", goalItem.ID, snapshotID).Scan(&referenceCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM criterion_verifications WHERE goal_id=$1 AND criterion_id=$2 AND criterion_version=$3 AND status='satisfied'", goalItem.ID, criterion.ID, criterion.Version).Scan(&verificationCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM execution_evidence ev JOIN tasks t ON t.id=ev.task_id WHERE t.goal_id=$1 AND ev.criterion_id=$2 AND ev.criterion_version=$3 AND ev.verifier_type='core.file-artifact'`, goalItem.ID, criterion.ID, criterion.Version).Scan(&evidenceCount); err != nil {
		t.Fatal(err)
	}
	if referenceCount != 1 || verificationCount != 1 || evidenceCount < 1 {
		t.Fatalf("pipeline records: snapshots=1 references=%d verifications=%d verifierEvidence=%d", referenceCount, verificationCount, evidenceCount)
	}
	var decisionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM supervisor_decisions WHERE goal_id=$1 AND decision_type='complete_goal'`, goalItem.ID).Scan(&decisionCount); err != nil {
		t.Fatal(err)
	}
	if decisionCount < 1 {
		t.Fatal("Supervisor did not persist a Goal completion Decision")
	}
	if !approvalSubmitted {
		t.Fatal("research Goal completed without the required report export Approval")
	}
	if _, err := dispatcher.DispatchBatch(ctx, uniqueLabel("research-e2e-final-dispatch"), 100, time.Second, 5); err != nil {
		t.Fatalf("flush persisted Approval/Execution Outbox events: %v", err)
	}
	var exportInvocationStatus, exportInputHash, exportApprovalID string
	var consumedAt sql.NullTime
	if err := pool.QueryRow(ctx, `SELECT i.status,i.input_hash,i.approval_id::text,a.consumed_at
		FROM tool_invocations i JOIN approvals a ON a.id=i.approval_id
		JOIN tasks t ON t.id=i.task_id WHERE t.goal_id=$1 AND i.tool_id='report.export'`, goalItem.ID).Scan(&exportInvocationStatus, &exportInputHash, &exportApprovalID, &consumedAt); err != nil {
		t.Fatalf("load approved report export invocation: %v", err)
	}
	if exportInvocationStatus != "succeeded" || exportApprovalID == "" || !consumedAt.Valid || exportInputHash == "" || exportInputHash != approvedParameterHash {
		t.Fatalf("report export invocation/approval lifecycle incomplete: status=%q approval=%q consumed=%v input_hash=%q", exportInvocationStatus, exportApprovalID, consumedAt, exportInputHash)
	}
	var approvalWakeCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM goal_job_wake_events w JOIN event_outbox e ON e.id=w.event_id
		WHERE w.goal_id=$1 AND e.event_type='approval.approved'`, goalItem.ID).Scan(&approvalWakeCount); err != nil || approvalWakeCount < 1 {
		t.Fatalf("Approval Outbox event did not wake the persistent Goal Job: receipts=%d err=%v", approvalWakeCount, err)
	}
	var reportHash string
	if err := pool.QueryRow(ctx, `SELECT ev.content_hash FROM execution_evidence ev JOIN tasks t ON t.id=ev.task_id WHERE t.goal_id=$1 AND ev.verifier_type='core.file-artifact' LIMIT 1`, goalItem.ID).Scan(&reportHash); err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyArtifactHash(ctx, criterion.ArtifactPath, reportHash); err != nil {
		t.Fatalf("final Artifact hash invalid: %v", err)
	}
	var sourceEvidenceCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM criterion_verification_evidence link
		JOIN criterion_verifications cv ON cv.id=link.verification_id
		JOIN execution_evidence ev ON ev.id=link.evidence_id
		WHERE cv.goal_id=$1 AND cv.criterion_id=$2 AND cv.criterion_version=$3
		AND ev.verifier_type='core.source-reference' AND ev.reference='source://'||$4::text||'#sha256='||$5`, goalItem.ID, criterion.ID, criterion.Version, snapshotID, sourceHash).Scan(&sourceEvidenceCount); err != nil || sourceEvidenceCount < 1 {
		t.Fatalf("Criterion Verification lacks snapshot-linked Core source Evidence: count=%d err=%v", sourceEvidenceCount, err)
	}
	published, err := os.ReadFile(filepath.Join(exportRoot, criterion.ArtifactPath))
	if err != nil || source.Hash(published) != reportHash {
		t.Fatalf("human-approved report export bytes/hash mismatch: hash=%s err=%v", source.Hash(published), err)
	}
	var publishedEvents int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM event_outbox WHERE aggregate_type='source_snapshots' AND aggregate_id=$1", snapshotID).Scan(&publishedEvents); err != nil || publishedEvents < 1 {
		t.Fatalf("Source Snapshot Outbox event count=%d err=%v", publishedEvents, err)
	}
}

type integrationGrants map[string]toolgateway.Grant

func (g integrationGrants) ResolveGrant(_ context.Context, _ toolgateway.Request, definition toolregistry.Definition) (toolgateway.Grant, error) {
	return g[definition.Identity.ProviderID+"/"+definition.Identity.ToolID+"@"+definition.Identity.Version], nil
}

type integrationResearchPlanner struct {
	sourceURL  string
	criterionID string
}

func (p integrationResearchPlanner) GenerateContext(ctx context.Context, input planner.GoalInput) (planner.Plan, error) {
	if err := ctx.Err(); err != nil {
		return planner.Plan{}, err
	}
	return planner.Plan{GoalID: input.ID, Summary: "Capture one allowlisted research source and create a source-bound report.", Tasks: []planner.TaskSpec{{
		Key: "research-report", Title: "Research and write the final report",
		Description: "Capture the operator-selected source and produce a Markdown report. source_url=" + p.sourceURL,
		CriterionIDs: []string{p.criterionID}, SuccessCriteria: []string{"Core verifies the report and its source citation"},
		EvidenceRequirements: []string{"Persist a real immutable source snapshot and bind an exact quotation"},
	}}, EstimatedHours: 0.1}, nil
}

type integrationResearchAgent struct{ sourceURL string }

func (a integrationResearchAgent) ID() string { return "research" }
func (a integrationResearchAgent) IdempotencyGuarantee() bool { return false }
func (a integrationResearchAgent) Execute(ctx context.Context, request agentruntime.Request) (agentruntime.Result, error) {
	return a.run(ctx, request, nil, nil)
}
func (a integrationResearchAgent) ExecuteResumable(ctx context.Context, request agentruntime.Request, checkpoint *agentruntime.Checkpoint, save agentruntime.CheckpointWriter, _ agentruntime.StepIdempotencyClaim) (agentruntime.Result, error) {
	return a.run(ctx, request, checkpoint, save)
}

type integrationResearchState struct {
	GoalID           string `json:"goal_id"`
	TaskID           string `json:"task_id"`
	ExecutionID      string `json:"execution_id"`
	SourceID        string `json:"source_id"`
	FinalURL        string `json:"final_url"`
	Quote           string `json:"quote"`
	ContentHash     string `json:"content_hash"`
	ArtifactHash    string `json:"artifact_hash"`
	ArtifactPath    string `json:"artifact_path"`
	CriterionVersion int   `json:"criterion_version"`
	ApprovalID      string `json:"approval_id,omitempty"`
}

func (a integrationResearchAgent) run(ctx context.Context, request agentruntime.Request, checkpoint *agentruntime.Checkpoint, save agentruntime.CheckpointWriter) (agentruntime.Result, error) {
	if request.Tools == nil || len(request.TaskCriterionIDs) != 1 || len(request.GoalCriteria) != 1 {
		return agentruntime.Result{}, errors.New("research pipeline requires the Core Tool Caller and one mapped criterion")
	}
	criterionID := request.TaskCriterionIDs[0]
	state := integrationResearchState{}
	completedSteps := []string{}
	if checkpoint != nil && len(checkpoint.Progress) > 0 {
		if err := json.Unmarshal(checkpoint.Progress, &state); err != nil || state.SourceID == "" || state.ArtifactHash == "" || state.ApprovalID == "" {
			return agentruntime.Result{}, fmt.Errorf("research checkpoint lacks the exact pending export Approval context: %w", err)
		}
		if state.GoalID != request.GoalID || state.TaskID != request.TaskID || state.ExecutionID != request.ExecutionID {
			return agentruntime.Result{}, errors.New("research checkpoint belongs to a different Goal/Task/Execution")
		}
		completedSteps = []string{"source-capture", "source-reference", "artifact-write", "report-export-wait"}
	} else {
		captureArgs, _ := json.Marshal(map[string]string{"url": a.sourceURL})
		captured, err := request.Tools.ExecuteTool(ctx, agentruntime.ToolCallRequest{ProviderID: "paios.native", ToolID: "source.capture", Version: "v1", StepID: "source-capture", IdempotencyKey: request.TaskID + ":source-capture", Arguments: captureArgs})
		if err != nil {
			return agentruntime.Result{}, err
		}
		var captureEnvelope struct{ Result string `json:"result"` }
		if err := json.Unmarshal(captured.Result, &captureEnvelope); err != nil {
			return agentruntime.Result{}, fmt.Errorf("decode Source Capture result: %w", err)
		}
		var capturedSource struct {
			SourceID    string `json:"source_id"`
			FinalURL    string `json:"final_url"`
			Content     string `json:"untrusted_source_content"`
			ContentHash string `json:"content_hash"`
		}
		if err := json.Unmarshal([]byte(captureEnvelope.Result), &capturedSource); err != nil || capturedSource.SourceID == "" || capturedSource.Content == "" {
			return agentruntime.Result{}, fmt.Errorf("Source Capture result has no durable snapshot/content: %w", err)
		}
		criterion := request.GoalCriteria[0]
		state.GoalID, state.TaskID, state.ExecutionID = request.GoalID, request.TaskID, request.ExecutionID
		state.SourceID, state.FinalURL, state.Quote, state.ContentHash = capturedSource.SourceID, capturedSource.FinalURL, firstQuote(capturedSource.Content, 120), capturedSource.ContentHash
		state.ArtifactPath, state.CriterionVersion = criterion.ArtifactPath, criterion.Version
		referenceArgs, _ := json.Marshal(map[string]string{"criterion_id": criterionID, "source_id": state.SourceID, "url": state.FinalURL, "quote": state.Quote})
		if _, err := request.Tools.ExecuteTool(ctx, agentruntime.ToolCallRequest{ProviderID: "paios.native", ToolID: "source.reference.add", Version: "v1", StepID: "source-reference", IdempotencyKey: request.TaskID + ":source-reference", Arguments: referenceArgs}); err != nil {
			return agentruntime.Result{}, err
		}
		report := fmt.Sprintf("# Summary\n\nThis report records an operator-selected source snapshot. Semantic conclusions require independent review.\n\n## Sources\n\n%s\n\n[Snapshot](source://%s) %s\n", state.Quote, state.SourceID, state.FinalURL)
		artifactArgs, _ := json.Marshal(map[string]string{"criterion_id": criterionID, "content": report})
		written, err := request.Tools.ExecuteTool(ctx, agentruntime.ToolCallRequest{ProviderID: "paios.native", ToolID: "artifact.write", Version: "v1", StepID: "artifact-write", IdempotencyKey: request.TaskID + ":artifact-write", Arguments: artifactArgs})
		if err != nil {
			return agentruntime.Result{}, err
		}
		var artifactEnvelope struct{ Result string `json:"result"` }
		if err := json.Unmarshal(written.Result, &artifactEnvelope); err != nil {
			return agentruntime.Result{}, fmt.Errorf("decode Artifact writer result: %w", err)
		}
		_, hash, found := strings.Cut(artifactEnvelope.Result, "#sha256=")
		if !found || len(hash) != 64 {
			return agentruntime.Result{}, errors.New("Artifact writer did not return a SHA-256 reference")
		}
		state.ArtifactHash = hash
		completedSteps = []string{"source-capture", "source-reference", "artifact-write"}
	}

	exportArgs, _ := json.Marshal(map[string]any{"criterion_id": criterionID, "criterion_version": state.CriterionVersion, "artifact_path": state.ArtifactPath, "sha256": state.ArtifactHash})
	export, err := request.Tools.ExecuteTool(ctx, agentruntime.ToolCallRequest{
		ProviderID: "paios.native", ToolID: "report.export", Version: "v1", StepID: "report-export",
		IdempotencyKey: request.TaskID + ":report-export", ApprovalID: state.ApprovalID, Arguments: exportArgs,
	})
	if err != nil {
		return agentruntime.Result{}, err
	}
	if export.Status == "waiting_approval" {
		state.ApprovalID = export.ApprovalID
		if save == nil || state.ApprovalID == "" {
			return agentruntime.Result{}, errors.New("pending report export Approval cannot be resumed without a persisted checkpoint")
		}
		if err := save(ctx, agentruntime.Checkpoint{StepID: "report-export-wait", Progress: researchCheckpointJSON(state), CompletedSteps: append(completedSteps, "report-export-wait"), SchemaVersion: 1}); err != nil {
			return agentruntime.Result{}, err
		}
		return agentruntime.Result{}, execution.Failure{Class: execution.FailureApprovalNeeded, Err: errors.New("report export is awaiting exact human Approval")}
	}
	if export.Status != "succeeded" {
		return agentruntime.Result{}, fmt.Errorf("report export returned unexpected status %q", export.Status)
	}
	if save != nil {
		if err := save(ctx, agentruntime.Checkpoint{StepID: "report-export-complete", Progress: researchCheckpointJSON(state), CompletedSteps: append(completedSteps, "report-export-complete"), SchemaVersion: 1}); err != nil {
			return agentruntime.Result{}, err
		}
	}
	return agentruntime.Result{Summary: "Captured an allowlisted source, bound its quote, wrote and independently verified a report, and exported it after exact human Approval.", Steps: 4, ToolCalls: 4, Outcome: agentruntime.OutcomeSucceeded}, nil
}

func firstQuote(value string, limit int) string {
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return strings.TrimSpace(string(runes))
}

func researchCheckpointJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

type integrationResearchExecutor struct {
	agents   *registry.Registry
	gateway  *toolgateway.Gateway
	verifier agentruntime.ResultVerifier
}

func (e integrationResearchExecutor) Execute(ctx context.Context, run execution.Run, checkpoint *execution.Checkpoint, progress execution.Progress) (execution.ExecutionResult, error) {
	var request agentruntime.Request
	if err := json.Unmarshal(run.Payload, &request); err != nil {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: err}
	}
	request.TaskID, request.GoalID, request.ExecutionID = run.TaskID, run.GoalID, run.ID
	request.Tools = &integrationToolCaller{gateway: e.gateway, run: run, progress: progress, maxCalls: request.MaxToolCalls}
	profile, err := e.agents.Get(run.AgentID)
	if err != nil {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: err}
	}
	resumable := profile.Agent.(agentruntime.ResumableAgent)
	var resume *agentruntime.Checkpoint
	if checkpoint != nil {
		resume = &agentruntime.Checkpoint{StepID: checkpoint.StepID, Progress: checkpoint.Progress, CompletedSteps: checkpoint.CompletedSteps, StateReference: checkpoint.StateReference, SchemaVersion: checkpoint.SchemaVersion}
	}
	result, err := resumable.ExecuteResumable(ctx, request, resume, func(ctx context.Context, checkpoint agentruntime.Checkpoint) error {
		return progress.SaveCheckpoint(ctx, execution.Checkpoint{StepID: checkpoint.StepID, Progress: checkpoint.Progress, CompletedSteps: checkpoint.CompletedSteps, StateReference: checkpoint.StateReference, SchemaVersion: checkpoint.SchemaVersion})
	}, progress.ClaimStep)
	if err != nil {
		return execution.ExecutionResult{}, err
	}
	verification, err := e.verifier.Verify(ctx, request, result)
	if err != nil || !verification.Verified || len(verification.Evidence) == 0 {
		return execution.ExecutionResult{}, execution.Failure{Class: execution.FailurePermanent, Err: fmt.Errorf("Core verifier rejected research result: %w", err)}
	}
	supervisorEvidence := make([]supervisor.Evidence, 0, len(verification.Evidence))
	executionEvidence := make([]execution.Evidence, 0, len(verification.Evidence))
	for _, item := range verification.Evidence {
		evidence := supervisor.Evidence{TaskID: run.TaskID, CriterionID: item.CriterionID, CriterionVersion: item.CriterionVersion, Source: item.Source, Description: item.Description, Reference: item.Reference, ContentHash: item.ContentHash, VerifierType: item.VerifierType, VerifierVersion: item.VerifierVersion, ObservedAt: time.Now().UTC()}
		supervisorEvidence = append(supervisorEvidence, evidence)
		executionEvidence = append(executionEvidence, execution.Evidence{CriterionID: item.CriterionID, CriterionVersion: item.CriterionVersion, Source: item.Source, Description: item.Description, Reference: item.Reference, ContentHash: item.ContentHash, VerifierType: item.VerifierType, VerifierVersion: item.VerifierVersion, ObservedAt: evidence.ObservedAt})
	}
	encoded, err := json.Marshal(supervisor.Execution{TaskID: run.TaskID, AgentID: profile.ID, Summary: result.Summary, Outcome: result.Outcome, ToolCalls: result.ToolCalls, Evidence: supervisorEvidence, CompletedAt: time.Now().UTC(), Verified: true, VerificationMethod: verification.Method})
	if err != nil {
		return execution.ExecutionResult{}, err
	}
	return execution.ExecutionResult{Result: encoded, Evidence: executionEvidence, VerifiedResult: true}, nil
}

type integrationToolCaller struct {
	gateway  *toolgateway.Gateway
	run      execution.Run
	progress execution.Progress
	maxCalls int
	calls    int
}

func (c *integrationToolCaller) ExecuteTool(ctx context.Context, call agentruntime.ToolCallRequest) (agentruntime.ToolCallResult, error) {
	if c.calls >= c.maxCalls {
		return agentruntime.ToolCallResult{}, agentruntime.ErrBudgetExceeded
	}
	c.calls++
	identity := toolregistry.Identity{ProviderID: call.ProviderID, ToolID: call.ToolID, Version: call.Version}
	if _, err := c.progress.ClaimStep(ctx, call.StepID, call.IdempotencyKey, toolgateway.RequestHash(identity, call.Arguments)); err != nil {
		return agentruntime.ToolCallResult{}, err
	}
	result, err := c.gateway.Execute(ctx, toolgateway.Request{
		GoalID: c.run.GoalID, TaskID: c.run.TaskID, ExecutionID: c.run.ID, StepID: call.StepID, AgentID: c.run.AgentID,
		Identity: identity, IdempotencyKey: call.IdempotencyKey, ApprovalID: call.ApprovalID,
		Lease: execution.Lease{ExecutionID: c.run.ID, WorkerID: c.run.WorkerID, Fencing: c.run.FencingToken, LeaseUntil: c.run.LeaseUntil}, Arguments: call.Arguments,
	})
	return agentruntime.ToolCallResult{InvocationID: result.InvocationID, Status: result.Status, ApprovalID: result.ApprovalID, Result: result.Result, Replayed: result.Replayed}, err
}

type integrationSourceCapture struct {
	ownerID    string
	onlyURL    string
	httpClient *http.Client
	repository source.Repository
}

func (h integrationSourceCapture) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("integration Source Capture requires Gateway context")
}
func (h integrationSourceCapture) ExecuteWithContext(ctx context.Context, invocation toolregistry.InvocationContext, args json.RawMessage) (json.RawMessage, error) {
	var input struct{ URL string `json:"url"` }
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, err
	}
	if input.URL != h.onlyURL || invocation.GoalID == "" || invocation.TaskID == "" {
		return nil, errors.New("integration source request did not match the explicitly chosen fixture")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, h.onlyURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := h.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/plain") {
		return nil, errors.New("integration source fixture returned unexpected status or MIME")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, source.MaxBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > source.MaxBodyBytes {
		return nil, fmt.Errorf("read bounded integration source response: %w", err)
	}
	parsed, err := url.Parse(h.onlyURL)
	if err != nil {
		return nil, err
	}
	normalized := source.NormalizeContent("text/plain", body)
	snapshot, err := h.repository.Create(ctx, source.Snapshot{
		OwnerID: h.ownerID, GoalID: invocation.GoalID, TaskID: invocation.TaskID,
		OriginalURL: h.onlyURL, FinalURL: h.onlyURL, HTTPStatus: response.StatusCode,
		MIMEType: "text/plain", RetrievedAt: time.Now().UTC(), ContentHash: source.Hash(body),
		RawContent: body, NormalizedContent: normalized, FetcherVersion: "controlled-http-fixture/v1",
		Metadata: map[string]string{"fixture_host": parsed.Hostname()},
	})
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(map[string]any{"source_id": snapshot.ID, "original_url": snapshot.OriginalURL, "final_url": snapshot.FinalURL, "http_status": snapshot.HTTPStatus, "mime_type": snapshot.MIMEType, "retrieved_at": snapshot.RetrievedAt, "content_hash": snapshot.ContentHash, "fetcher_version": snapshot.FetcherVersion, "untrusted_source_content": snapshot.NormalizedContent})
	if err != nil {
		return nil, err
	}
	wrapped, err := json.Marshal(map[string]string{"result": string(encoded)})
	return wrapped, err
}

type eventDispatcherFixture struct{ repository *store.OutboxDispatcherStore }

func (d *eventDispatcherFixture) run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		_, _ = d.repository.DispatchBatch(ctx, "research-e2e-dispatcher", 50, time.Second, 5)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

var _ agentruntime.Agent = integrationResearchAgent{}
var _ agentruntime.ResumableAgent = integrationResearchAgent{}
var _ execution.Executor = integrationResearchExecutor{}
