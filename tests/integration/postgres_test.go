// English: These opt-in tests exercise migrations, durable domain repositories, constraints, transactions, and process recovery against PostgreSQL.
// 中文：这些显式启用的集成测试使用真实 PostgreSQL 验证迁移、领域仓储、约束、事务及进程恢复。
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/approval"
	"personal-ai-os/internal/app/database"
	"personal-ai-os/internal/app/store"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/knowledge"
	"personal-ai-os/internal/memory"
	"personal-ai-os/internal/node"
	"personal-ai-os/internal/policy"
	"personal-ai-os/internal/supervisor"
	"personal-ai-os/internal/task"
	toolpolicy "personal-ai-os/internal/tool/policy"
	"personal-ai-os/migrations"
	paierrors "personal-ai-os/pkg/errors"
)

const testOwnerID = store.LocalOwnerID

func integrationDB(t *testing.T) (*pgxpool.Pool, *store.Postgres) {
	t.Helper()
	databaseURL := os.Getenv("PAIOS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set PAIOS_TEST_DATABASE_URL to a dedicated disposable PostgreSQL test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	var databaseName string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&databaseName); err != nil {
		pool.Close()
		t.Fatalf("read test database name: %v", err)
	}
	if !strings.Contains(strings.ToLower(databaseName), "test") {
		pool.Close()
		t.Fatalf("refusing to run destructive-cleanup-capable integration tests against database %q; name must contain 'test'", databaseName)
	}
	t.Cleanup(pool.Close)
	return pool, store.NewPostgres(pool)
}

func TestPostgresMigrationsFromEmptyDatabaseAndIdempotency(t *testing.T) {
	pool, _ := integrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var migrationTableExists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.schema_migrations') IS NOT NULL").Scan(&migrationTableExists); err != nil {
		t.Fatalf("inspect initial migration history: %v", err)
	}
	versionsBefore := 0
	if migrationTableExists {
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&versionsBefore); err != nil {
			t.Fatalf("read initial migration history: %v", err)
		}
	}
	t.Logf("migration versions present before application: %d (isolated test target starts with zero)", versionsBefore)
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply ordered migrations: %v", err)
	}

	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("reapply migrations: %v", err)
	}
	expected := []string{
		"0001_core.sql",
		"0002_execution_approval_memory.sql",
		"0003_goal_evidence.sql",
		"0004_knowledge_vectors.sql",
		"0005_task_attempts.sql",
		"0006_nodes.sql",
		"0007_model_usage.sql",
		"0008_durable_execution_runtime.sql",
	}
	rows, err := pool.Query(ctx, "SELECT version,checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("query migration history: %v", err)
	}
	var got []string
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			t.Fatalf("scan migration history: %v", err)
		}
		if len(checksum) != 64 {
			rows.Close()
			t.Fatalf("migration %s has invalid SHA-256 checksum length %d", version, len(checksum))
		}
		got = append(got, version)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate migration history: %v", err)
	}
	rows.Close()
	if fmt.Sprint(got) != fmt.Sprint(expected) {
		t.Fatalf("migration order/history = %v, want %v", got, expected)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != len(expected) {
		t.Fatalf("migration count=%d err=%v, want %d", count, err, len(expected))
	}
	var vectorVersion string
	if err := pool.QueryRow(ctx, "SELECT extversion FROM pg_extension WHERE extname='vector'").Scan(&vectorVersion); err != nil {
		t.Fatalf("pgvector extension is unavailable: %v", err)
	}
	t.Logf("pgvector extension version: %s", vectorVersion)
	assertMigrationFailureRollsBack(t, ctx, pool, os.Getenv("PAIOS_TEST_DATABASE_URL"))

	for _, index := range []string{"goals_owner_status_idx", "tasks_goal_status_idx", "execution_runs_recovery_idx", "memories_search_idx", "knowledge_chunks_search_idx", "nodes_status_type_idx"} {
		var exists bool
		if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname=current_schema() AND indexname=$1)", index).Scan(&exists); err != nil || !exists {
			t.Errorf("expected index %s, exists=%v err=%v", index, exists, err)
		}
	}
}

func assertMigrationFailureRollsBack(t *testing.T, ctx context.Context, adminPool *pgxpool.Pool, databaseURL string) {
	t.Helper()
	databaseName := fmt.Sprintf("paios_migration_rollback_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+databaseName); err != nil {
		t.Fatalf("create isolated database for migration rollback test: %v", err)
	}
	var migrationPool *pgxpool.Pool
	cleanup := func() {
		if migrationPool != nil {
			migrationPool.Close()
		}
		terminateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = adminPool.Exec(terminateCtx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid<>pg_backend_pid()", databaseName)
		_, _ = adminPool.Exec(terminateCtx, "DROP DATABASE IF EXISTS "+databaseName)
	}
	t.Cleanup(cleanup)
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse integration database URL: %v", err)
	}
	config.ConnConfig.Database = databaseName
	migrationPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("create isolated migration pool: %v", err)
	}
	if err := migrationPool.Ping(ctx); err != nil {
		t.Fatalf("connect to isolated migration database: %v", err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	functionName := "paios_test_fail_migration_" + suffix
	triggerName := "paios_test_fail_migration_trigger_" + suffix
	if _, err := migrationPool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected migration failure' USING ERRCODE='23514'; END $$`, functionName)); err != nil {
		t.Fatalf("create migration failure function: %v", err)
	}
	if _, err := migrationPool.Exec(ctx, fmt.Sprintf("CREATE EVENT TRIGGER %s ON ddl_command_start WHEN TAG IN ('CREATE INDEX') EXECUTE FUNCTION %s()", triggerName, functionName)); err != nil {
		t.Fatalf("create migration failure event trigger: %v", err)
	}
	if err := migrations.Apply(ctx, migrationPool); err == nil {
		t.Fatal("expected injected failure while applying migration indexes")
	}
	var migrationTableExists, usersTableExists bool
	if err := migrationPool.QueryRow(ctx, "SELECT to_regclass('public.schema_migrations') IS NOT NULL, to_regclass('public.users') IS NOT NULL").Scan(&migrationTableExists, &usersTableExists); err != nil {
		t.Fatalf("inspect rolled-back migration schema: %v", err)
	}
	if migrationTableExists || usersTableExists {
		t.Fatalf("migration failure left partial schema: schema_migrations=%v users=%v", migrationTableExists, usersTableExists)
	}
	if _, err := migrationPool.Exec(ctx, "DROP EVENT TRIGGER "+triggerName); err != nil {
		t.Fatalf("drop migration failure event trigger: %v", err)
	}
	if _, err := migrationPool.Exec(ctx, "DROP FUNCTION "+functionName+"()"); err != nil {
		t.Fatalf("drop migration failure function: %v", err)
	}
	if err := migrations.Apply(ctx, migrationPool); err != nil {
		t.Fatalf("apply migrations after removing injected failure: %v", err)
	}
	var versionCount int
	if err := migrationPool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&versionCount); err != nil || versionCount != 8 {
		t.Fatalf("successful retry migration count=%d err=%v, want 8", versionCount, err)
	}
	t.Log("injected migration failure rolled back both schema changes and version rows; clean retry applied all eight migrations")
}

func TestPostgresCorePersistenceConstraintsAndTransactions(t *testing.T) {
	pool, repository := integrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply migrations for core integration test: %v", err)
	}

	goalIDs := []string{}
	taskIDs := []string{}
	memoryIDs := []string{}
	documentIDs := []string{}
	nodeIDs := []string{}
	approvalIDs := []string{}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		cleanupIntegrationRows(t, cleanupCtx, pool, goalIDs, taskIDs, memoryIDs, documentIDs, nodeIDs, approvalIDs)
	})

	createdGoal, err := repository.Create(ctx, goal.Goal{
		OwnerID: testOwnerID, Title: uniqueLabel("postgres-goal"), Description: "db persistence check",
		Priority: 4, Status: goal.Draft, SuccessCriteria: []string{"persist evidence"},
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	goalIDs = append(goalIDs, createdGoal.ID)
	loadedGoal, err := repository.Get(ctx, testOwnerID, createdGoal.ID)
	if err != nil || loadedGoal.ID != createdGoal.ID || loadedGoal.CreatedAt.IsZero() {
		t.Fatalf("load created goal=%+v err=%v", loadedGoal, err)
	}
	loadedGoal.Description = "updated description"
	loadedGoal.Priority = 5
	updatedGoal, err := repository.Update(ctx, testOwnerID, loadedGoal)
	if err != nil || updatedGoal.Description != "updated description" || updatedGoal.Priority != 5 {
		t.Fatalf("update goal=%+v err=%v", updatedGoal, err)
	}
	if _, err := repository.Transition(ctx, testOwnerID, createdGoal.ID, goal.Active, nil); err != nil {
		t.Fatalf("activate goal: %v", err)
	}

	tasks := repository.Tasks()
	createdTask, err := tasks.Create(ctx, task.Task{GoalID: createdGoal.ID, Title: uniqueLabel("postgres-task"), Priority: 3, Status: task.Created})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	taskIDs = append(taskIDs, createdTask.ID)
	if _, err := tasks.Transition(ctx, createdTask.ID, task.Ready); err != nil {
		t.Fatalf("make task ready: %v", err)
	}
	startedTask, err := tasks.Transition(ctx, createdTask.ID, task.Running)
	if err != nil || startedTask.AttemptCount != 1 || startedTask.StartedAt == nil {
		t.Fatalf("start task=%+v err=%v", startedTask, err)
	}
	if _, offset := startedTask.StartedAt.Zone(); offset != 0 {
		t.Fatalf("task timestamp is not UTC: %v", startedTask.StartedAt)
	}

	// A failed dependency insertion must roll back every task inserted earlier in the same plan transaction.
	beforeTasks := countForGoal(t, ctx, pool, "tasks", "goal_id", createdGoal.ID)
	_, err = repository.CreateTasks(ctx, []task.Task{
		{GoalID: createdGoal.ID, PlanKey: "valid", Title: uniqueLabel("rollback-task"), Priority: 3, Status: task.Created},
		{GoalID: createdGoal.ID, PlanKey: "dependent", Title: uniqueLabel("rollback-dependent"), Priority: 3, Status: task.Created, DependsOn: []string{"missing-plan-key"}},
	})
	if err == nil {
		t.Fatal("expected unknown plan dependency to fail")
	}
	if after := countForGoal(t, ctx, pool, "tasks", "goal_id", createdGoal.ID); after != beforeTasks {
		t.Fatalf("failed task plan left partial rows: before=%d after=%d", beforeTasks, after)
	}

	// The task row and its goal relationship are protected by a real PostgreSQL FK.
	_, err = tasks.Create(ctx, task.Task{GoalID: "ffffffff-ffff-4fff-8fff-ffffffffffff", Title: uniqueLabel("orphan-task"), Priority: 3, Status: task.Created})
	assertPostgresCode(t, err, "23503", "task-to-goal foreign key")

	executions := store.NewExecutionStore(pool)
	verifiedExecution := supervisor.Execution{
		TaskID: createdTask.ID, AgentID: "integration-agent", Summary: "integration verified result",
		Outcome: agentruntime.OutcomeSucceeded, ModelCalls: 1, InputTokens: 5, OutputTokens: 4, TotalTokens: 9,
		Evidence: []supervisor.Evidence{{TaskID: createdTask.ID, Source: "integration-test", Description: "database-backed result evidence", ObservedAt: time.Now().UTC()}},
		CompletedAt: time.Now().UTC(),
	}
	savedExecution, err := executions.Save(ctx, verifiedExecution)
	if err != nil {
		t.Fatalf("save execution: %v", err)
	}
	if savedExecution.ID == "" {
		t.Fatal("execution save did not return a run id")
	}
	executionList, err := executions.ListByGoal(ctx, createdGoal.ID)
	if err != nil || len(executionList) != 1 || executionList[0].TaskID != createdTask.ID {
		t.Fatalf("list executions=%+v err=%v", executionList, err)
	}
	var executionEvidence int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM execution_evidence WHERE execution_run_id=$1", savedExecution.ID).Scan(&executionEvidence); err != nil || executionEvidence != 1 {
		t.Fatalf("execution evidence count=%d err=%v", executionEvidence, err)
	}
	usageTotals, err := store.NewModelUsageStore(pool).ModelUsage(ctx, createdGoal.ID)
	if err != nil || usageTotals.ModelCalls != 1 || usageTotals.ModelTokens != 9 {
		t.Fatalf("model usage=%+v err=%v", usageTotals, err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO execution_runs(task_id,status,idempotency_key,input_hash,attempt)
		VALUES($1,'failed',$2,$3,1)`, createdTask.ID, "mock-execution:"+createdTask.ID, strings.Repeat("a", 64))
	assertPostgresCode(t, err, "23505", "execution idempotency unique constraint")
	if _, err := tasks.Transition(ctx, createdTask.ID, task.Completed); err != nil {
		t.Fatalf("complete task after durable execution: %v", err)
	}

	// A database error after execution/evidence/outbox inserts must roll the entire execution transaction back.
	rollbackTask, err := tasks.Create(ctx, task.Task{GoalID: createdGoal.ID, Title: uniqueLabel("execution-rollback-task"), Priority: 2, Status: task.Created})
	if err != nil {
		t.Fatalf("create rollback task: %v", err)
	}
	taskIDs = append(taskIDs, rollbackTask.ID)
	if _, err := tasks.Transition(ctx, rollbackTask.ID, task.Ready); err != nil {
		t.Fatalf("make rollback task ready: %v", err)
	}
	if _, err := tasks.Transition(ctx, rollbackTask.ID, task.Running); err != nil {
		t.Fatalf("make rollback task recoverable: %v", err)
	}
	rollbackExecution := supervisor.Execution{
		TaskID: rollbackTask.ID, AgentID: "integration-agent", Summary: "must roll back",
		Outcome: agentruntime.OutcomeSucceeded, Evidence: []supervisor.Evidence{{TaskID: rollbackTask.ID, Source: "test", Description: "rollback evidence", ObservedAt: time.Now().UTC()}}, CompletedAt: time.Now().UTC(),
	}
	withAuditFailure(t, ctx, pool, "execution.succeeded", func() {
		if _, err := executions.Save(ctx, rollbackExecution); err == nil {
			t.Fatal("expected injected audit failure during execution persistence")
		}
	})
	var runCount, evidenceCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM execution_runs WHERE task_id=$1", rollbackTask.ID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM execution_evidence e JOIN execution_runs r ON r.id=e.execution_run_id WHERE r.task_id=$1", rollbackTask.ID).Scan(&evidenceCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 || evidenceCount != 0 {
		t.Fatalf("execution transaction partially committed: runs=%d evidence=%d", runCount, evidenceCount)
	}
	recoverable, err := repository.Recoverable(ctx, 100)
	if err != nil {
		t.Fatalf("query recoverable tasks: %v", err)
	}
	foundRecoverable := false
	for _, item := range recoverable {
		if item.ID == rollbackTask.ID && item.Status == task.Running {
			foundRecoverable = true
		}
	}
	if !foundRecoverable {
		t.Fatal("task without an execution record was not observable for recovery checks")
	}
	if _, err := tasks.Transition(ctx, rollbackTask.ID, task.Failed); err != nil {
		t.Fatalf("close failed execution fixture: %v", err)
	}

	// A failed audit insert must roll back the Goal, its outbox event, and its audit row together.
	rollbackTitle := uniqueLabel("goal-transaction-rollback")
	withAuditFailure(t, ctx, pool, "goal.created", func() {
		_, err := repository.Create(ctx, goal.Goal{OwnerID: testOwnerID, Title: rollbackTitle, Priority: 3, Status: goal.Draft})
		if err == nil {
			t.Fatal("expected injected audit failure during Goal creation")
		}
	})
	assertZero(t, ctx, pool, "SELECT count(*) FROM goals WHERE title=$1", rollbackTitle)
	assertZero(t, ctx, pool, "SELECT count(*) FROM event_outbox WHERE event_type='goal.created' AND payload->>'title'=$1", rollbackTitle)
	assertZero(t, ctx, pool, "SELECT count(*) FROM audit_records WHERE action='goal.created' AND metadata->>'title'=$1", rollbackTitle)

	// Persist a Goal approval, verify A3 gating, then race two decisions against the same locked row.
	approvalItem, err := approval.New(testOwnerID, createdTask.ID, "integration-tool", "test-action", map[string]any{"value": "exact"}, 3, time.Now().Add(time.Hour), time.Now())
	if err != nil {
		t.Fatalf("create approval request value: %v", err)
	}
	approvalRepository := repository.Approvals()
	createdApproval, err := approvalRepository.Create(ctx, approvalItem)
	if err != nil {
		t.Fatalf("persist approval: %v", err)
	}
	approvalIDs = append(approvalIDs, createdApproval.ID)
	policyDecision, err := policy.Evaluate(policy.Request{ToolID: "integration-tool", Action: "test-action", Level: policy.A3, ToolAllowed: true})
	if err != nil || policyDecision.Decision != policy.RequireApproval {
		t.Fatalf("A3 policy decision=%+v err=%v", policyDecision, err)
	}
	gate := toolpolicy.Executor{}
	tool := &countingTool{}
	if _, err := gate.Execute(ctx, "integration-tool", "test-action", policy.A3, true, false, map[string]any{"value": "exact"}, nil, tool); !errors.Is(err, toolpolicy.ErrApprovalRequired) {
		t.Fatalf("A3 gate without approval error=%v", err)
	}
	if tool.calls != 0 {
		t.Fatal("tool executed before approval")
	}
	invalidTaskApproval, err := approval.New(testOwnerID, "ffffffff-ffff-4fff-8fff-ffffffffffff", "integration-tool", "test-action", map[string]any{"value": "exact"}, 3, time.Now().Add(time.Hour), time.Now())
	if err != nil {
		t.Fatalf("construct invalid task approval: %v", err)
	}
	_, err = approvalRepository.Create(ctx, invalidTaskApproval)
	assertPostgresCode(t, err, "23503", "approval-to-task foreign key")
	withAuditFailure(t, ctx, pool, "approval.approved", func() {
		_, err := approvalRepository.Decide(ctx, testOwnerID, createdApproval.ID, approval.Approved, time.Now().UTC())
		if err == nil {
			t.Fatal("expected injected audit failure during approval decision")
		}
	})
	stillPending, err := approvalRepository.Get(ctx, testOwnerID, createdApproval.ID)
	if err != nil || stillPending.Status != approval.Pending {
		t.Fatalf("approval changed despite failed audit transaction: %+v err=%v", stillPending, err)
	}
	assertCount(t, ctx, pool, "SELECT count(*) FROM event_outbox WHERE aggregate_id=$1", createdApproval.ID, 1)
	assertCount(t, ctx, pool, "SELECT count(*) FROM audit_records WHERE resource_id=$1", createdApproval.ID, 1)

	startRace := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, decision := range []approval.Status{approval.Approved, approval.Rejected} {
		wg.Add(1)
		go func(decision approval.Status) {
			defer wg.Done()
			<-startRace
			_, err := approvalRepository.Decide(ctx, testOwnerID, createdApproval.ID, decision, time.Now().UTC())
			results <- err
		}(decision)
	}
	close(startRace)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, approval.ErrInvalidTransition) {
			t.Errorf("unexpected concurrent approval decision error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent approval decisions succeeded %d times, want exactly once", successes)
	}
	persistedApproval, err := approvalRepository.Get(ctx, testOwnerID, createdApproval.ID)
	if err != nil || (persistedApproval.Status != approval.Approved && persistedApproval.Status != approval.Rejected) {
		t.Fatalf("persisted approval=%+v err=%v", persistedApproval, err)
	}
	assertCount(t, ctx, pool, "SELECT count(*) FROM event_outbox WHERE aggregate_id=$1", createdApproval.ID, 2)
	assertCount(t, ctx, pool, "SELECT count(*) FROM audit_records WHERE resource_id=$1", createdApproval.ID, 2)
	gateApprovalValue, err := approval.New(testOwnerID, createdTask.ID, "integration-tool", "verified-action", map[string]any{"value": "approved"}, 3, time.Now().Add(time.Hour), time.Now())
	if err != nil {
		t.Fatalf("construct gate approval: %v", err)
	}
	gateApproval, err := approvalRepository.Create(ctx, gateApprovalValue)
	if err != nil {
		t.Fatalf("persist gate approval: %v", err)
	}
	approvalIDs = append(approvalIDs, gateApproval.ID)
	gateApproval, err = approvalRepository.Decide(ctx, testOwnerID, gateApproval.ID, approval.Approved, time.Now().UTC())
	if err != nil {
		t.Fatalf("approve exact tool action: %v", err)
	}
	if _, err := gate.Execute(ctx, "integration-tool", "verified-action", policy.A3, true, false, map[string]any{"value": "changed"}, &gateApproval, tool); !errors.Is(err, approval.ErrParametersChanged) {
		t.Fatalf("changed action parameters were not rejected: %v", err)
	}
	if tool.calls != 0 {
		t.Fatal("tool executed with changed approved parameters")
	}
	if _, err := gate.Execute(ctx, "integration-tool", "verified-action", policy.A3, true, false, map[string]any{"value": "approved"}, &gateApproval, tool); err != nil {
		t.Fatalf("execute exact approved action: %v", err)
	}
	if tool.calls != 1 {
		t.Fatalf("tool calls=%d, want one call after exact approval", tool.calls)
	}

	// Exercise Memory CRUD/full-text search/soft delete against PostgreSQL.
	memories := repository.Memories()
	createdMemory, err := memories.Create(ctx, memory.Memory{OwnerID: testOwnerID, Category: memory.Fact, Content: uniqueLabel("cobalt-memory-search"), Source: "integration-test", Confidence: 0.9, Metadata: map[string]any{"origin": "test"}})
	if err != nil {
		t.Fatalf("create memory: %v", err)
	}
	memoryIDs = append(memoryIDs, createdMemory.ID)
	searchedMemories, err := memories.Search(ctx, testOwnerID, "cobalt-memory-search", 10)
	if err != nil || len(searchedMemories) != 1 || searchedMemories[0].ID != createdMemory.ID {
		t.Fatalf("search memories=%+v err=%v", searchedMemories, err)
	}
	createdMemory.Content = "updated cobalt preference"
	createdMemory.Category = memory.Preference
	updatedMemory, err := memories.Update(ctx, testOwnerID, createdMemory)
	if err != nil || updatedMemory.Category != memory.Preference {
		t.Fatalf("update memory=%+v err=%v", updatedMemory, err)
	}
	if err := memories.Delete(ctx, testOwnerID, createdMemory.ID); err != nil {
		t.Fatalf("soft delete memory: %v", err)
	}
	if _, err := memories.Get(ctx, testOwnerID, createdMemory.ID); !errors.Is(err, paierrors.ErrNotFound) {
		t.Fatalf("soft-deleted memory get error=%v, want not found", err)
	}
	_, err = repository.CreateMemory(ctx, memory.Memory{OwnerID: testOwnerID, Category: "unsupported", Content: "invalid-category", Source: "test", Confidence: .5})
	assertPostgresCode(t, err, "23514", "memory category check")

	// Knowledge import, deduplication, chunk persistence, and cited full-text retrieval.
	knowledgeRepository := repository.Knowledge()
	content := "PostgreSQL knowledge retrieval marker cobalt systems"
	document := knowledge.Document{OwnerID: testOwnerID, Title: uniqueLabel("knowledge-title"), Source: "integration-test", ContentHash: knowledge.HashContent(content), Chunks: []knowledge.Chunk{{Index: 0, Content: content, Metadata: map[string]any{"part": 1}}}, Metadata: map[string]any{"kind": "test"}}
	createdDocument, err := knowledgeRepository.Import(ctx, document)
	if err != nil {
		t.Fatalf("import knowledge document: %v", err)
	}
	documentIDs = append(documentIDs, createdDocument.ID)
	if _, err := knowledgeRepository.Import(ctx, document); !errors.Is(err, paierrors.ErrConflict) {
		t.Fatalf("duplicate knowledge import error=%v, want conflict", err)
	}
	citations, err := knowledgeRepository.Search(ctx, testOwnerID, "cobalt systems", 10)
	if err != nil || len(citations) != 1 || citations[0].DocumentID != createdDocument.ID || citations[0].ChunkIndex != 0 {
		t.Fatalf("knowledge citations=%+v err=%v", citations, err)
	}

	// Node registration is durable, duplicate identity intentionally upserts, and heartbeat changes status.
	nodes := repository.Nodes()
	nodeIdentity := uniqueLabel("node-identity")
	createdNode, err := nodes.Register(ctx, node.Node{Identity: nodeIdentity, Name: "Integration Worker", Type: node.Worker, Capabilities: []string{"db-test"}})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	nodeIDs = append(nodeIDs, createdNode.ID)
	upsertedNode, err := nodes.Register(ctx, node.Node{Identity: nodeIdentity, Name: "Integration Worker Updated", Type: node.Worker, Capabilities: []string{"db-test", "heartbeat"}})
	if err != nil || upsertedNode.ID != createdNode.ID || upsertedNode.Status != node.Offline {
		t.Fatalf("node upsert=%+v err=%v", upsertedNode, err)
	}
	heartbeatAt := time.Now().UTC().Add(-time.Second)
	heartbeatedNode, err := nodes.Heartbeat(ctx, createdNode.ID, node.Online, heartbeatAt)
	if err != nil || heartbeatedNode.Status != node.Online || heartbeatedNode.LastHeartbeat == nil || !heartbeatedNode.LastHeartbeat.Equal(heartbeatAt) {
		t.Fatalf("node heartbeat=%+v err=%v", heartbeatedNode, err)
	}
	verifyDurableClaimCheckpointFencing(t, ctx, pool, repository, createdGoal.ID, &taskIDs)

	// Goal evidence and status transition commit atomically; retrieval includes persisted evidence.
	completedGoal, err := repository.Transition(ctx, testOwnerID, createdGoal.ID, goal.Completed, []goal.Evidence{{Source: "integration-test", Description: "verified database record", Reference: "db://goal", ObservedAt: time.Now().UTC()}})
	if err != nil || completedGoal.Progress != 100 || len(completedGoal.Evidence) != 1 {
		t.Fatalf("complete Goal=%+v err=%v", completedGoal, err)
	}
	storedGoal, err := repository.Get(ctx, testOwnerID, createdGoal.ID)
	if err != nil || storedGoal.Status != goal.Completed || len(storedGoal.Evidence) != 1 || storedGoal.CompletedAt == nil {
		t.Fatalf("reloaded completed Goal=%+v err=%v", storedGoal, err)
	}
	var evidenceRows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM goal_evidence WHERE goal_id=$1", createdGoal.ID).Scan(&evidenceRows); err != nil || evidenceRows != 1 {
		t.Fatalf("goal evidence rows=%d err=%v", evidenceRows, err)
	}

	// Verify committed Goal/Task/Execution mutations have matching audit and outbox records.
	for _, id := range []string{createdGoal.ID, createdTask.ID, savedExecution.ID} {
		var outboxCount, auditCount int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM event_outbox WHERE aggregate_id=$1", id).Scan(&outboxCount); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_records WHERE resource_id=$1", id).Scan(&auditCount); err != nil {
			t.Fatal(err)
		}
		if outboxCount == 0 || auditCount == 0 {
			t.Errorf("aggregate %s mutation journal incomplete: outbox=%d audit=%d", id, outboxCount, auditCount)
		}
	}
}

func verifyDurableClaimCheckpointFencing(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repository *store.Postgres, goalID string, taskIDs *[]string) {
	t.Helper()
	durable := store.NewDurableExecutionStore(pool)
	taskRepo := repository.Tasks()
	runtimeTask, err := taskRepo.Create(ctx, task.Task{GoalID: goalID, Title: uniqueLabel("durable-claim"), Priority: 3, Status: task.Ready})
	if err != nil {
		t.Fatalf("create durable execution task: %v", err)
	}
	*taskIDs = append(*taskIDs, runtimeTask.ID)
	request := newExecutionEnqueueRequest(runtimeTask.ID, "test-executor", "attempt-0", []byte(`{"operation":"integration"}`), 3, true)
	queued, replayed, err := durable.Enqueue(ctx, request)
	if err != nil || replayed || queued.Status != execution.StatusQueued {
		t.Fatalf("enqueue durable run=%+v replay=%v err=%v", queued, replayed, err)
	}
	duplicate, replayed, err := durable.Enqueue(ctx, request)
	if err != nil || !replayed || duplicate.ID != queued.ID {
		t.Fatalf("idempotent enqueue=%+v replay=%v err=%v", duplicate, replayed, err)
	}
	changed := request
	changed.Payload = []byte(`{"operation":"different"}`)
	changed.InputHash = fmt.Sprintf("%x", sha256.Sum256(changed.Payload))
	if _, _, err := durable.Enqueue(ctx, changed); !errors.Is(err, execution.ErrIdempotencyReuse) {
		t.Fatalf("idempotency key with changed input error=%v", err)
	}

	start := make(chan struct{})
	claimResults := make(chan struct {
		run execution.Run
		err error
	}, 2)
	for _, workerID := range []string{"claim-worker-a", "claim-worker-b"} {
		go func(workerID string) {
			<-start
			run, err := durable.Claim(ctx, workerID, 3*time.Second)
			claimResults <- struct {
				run execution.Run
				err error
			}{run: run, err: err}
		}(workerID)
	}
	close(start)
	first := <-claimResults
	second := <-claimResults
	var claimed execution.Run
	if first.err == nil {
		claimed = first.run
		if !errors.Is(second.err, execution.ErrNoExecution) {
			t.Fatalf("second concurrent claimant returned run=%+v err=%v", second.run, second.err)
		}
	} else if second.err == nil {
		claimed = second.run
		if !errors.Is(first.err, execution.ErrNoExecution) {
			t.Fatalf("first concurrent claimant returned run=%+v err=%v", first.run, first.err)
		}
	} else {
		t.Fatalf("both workers failed to claim one queued execution: %v / %v", first.err, second.err)
	}
	if claimed.ID != queued.ID || claimed.FencingToken != 1 || claimed.Status != execution.StatusClaimed {
		t.Fatalf("claimed run=%+v", claimed)
	}
	oldLease := execution.Lease{ExecutionID: claimed.ID, WorkerID: claimed.WorkerID, Fencing: claimed.FencingToken, LeaseUntil: claimed.LeaseUntil}
	if err := durable.Start(ctx, oldLease); err != nil {
		t.Fatalf("start claimed run: %v", err)
	}
	if created, err := durable.ClaimStep(ctx, oldLease, "step-1", "tool-call-1", strings.Repeat("c", 64)); err != nil || !created {
		t.Fatalf("first step idempotency claim created=%v err=%v", created, err)
	}
	if created, err := durable.ClaimStep(ctx, oldLease, "step-1", "tool-call-1", strings.Repeat("c", 64)); err != nil || created {
		t.Fatalf("duplicate step claim created=%v err=%v", created, err)
	}
	if _, err := durable.ClaimStep(ctx, oldLease, "step-1", "tool-call-1", strings.Repeat("d", 64)); !errors.Is(err, execution.ErrIdempotencyReuse) {
		t.Fatalf("step key with different input error=%v", err)
	}
	firstCheckpoint, err := durable.SaveCheckpoint(ctx, oldLease, execution.Checkpoint{StepID: "step-1", Progress: []byte(`{"cursor":1}`), CompletedSteps: []string{"step-1"}, SchemaVersion: 1})
	if err != nil || firstCheckpoint.Sequence != 1 {
		t.Fatalf("save first checkpoint=%+v err=%v", firstCheckpoint, err)
	}
	secondCheckpoint, err := durable.SaveCheckpoint(ctx, oldLease, execution.Checkpoint{StepID: "step-2", Progress: []byte(`{"cursor":2}`), CompletedSteps: []string{"step-1", "step-2"}, StateReference: "file://checkpoint-ref", SchemaVersion: 1})
	if err != nil || secondCheckpoint.Sequence != 2 {
		t.Fatalf("save second checkpoint=%+v err=%v", secondCheckpoint, err)
	}
	latest, err := durable.LatestCheckpoint(ctx, claimed.ID)
	if err != nil || latest.Sequence != 2 || latest.StepID != "step-2" || len(latest.CompletedSteps) != 2 {
		t.Fatalf("latest checkpoint=%+v err=%v", latest, err)
	}
	if _, err := durable.SaveCheckpoint(ctx, oldLease, execution.Checkpoint{StepID: "secret-step", Progress: []byte(`{"api_key":"should-not-persist"}`), SchemaVersion: 1}); err == nil {
		t.Fatal("checkpoint with secret-like field should be rejected")
	}
	assertCheckpointFailureRollsBack(t, ctx, pool, durable, oldLease, claimed.ID)

	if _, err := pool.Exec(ctx, "UPDATE execution_runs SET lease_until=now()-interval '1 second' WHERE id=$1", claimed.ID); err != nil {
		t.Fatalf("expire execution lease for recovery test: %v", err)
	}
	if _, err := durable.Heartbeat(ctx, oldLease, 3*time.Second); !errors.Is(err, execution.ErrLeaseLost) {
		t.Fatalf("expired lease heartbeat error=%v", err)
	}
	if recovered, err := durable.RecoverExpired(ctx, 10, 0); err != nil || recovered != 1 {
		t.Fatalf("first expired lease recovery count=%d err=%v", recovered, err)
	}
	if recovered, err := durable.RecoverExpired(ctx, 10, 0); err != nil || recovered != 0 {
		t.Fatalf("repeated recovery was not idempotent: count=%d err=%v", recovered, err)
	}
	recoveredRun, err := durable.Get(ctx, claimed.ID)
	if err != nil || recoveredRun.Status != execution.StatusRetryWait {
		t.Fatalf("recovered execution=%+v err=%v", recoveredRun, err)
	}
	newClaim, err := durable.Claim(ctx, "replacement-worker", 3*time.Second)
	if err != nil || newClaim.ID != claimed.ID || newClaim.FencingToken <= oldLease.Fencing || newClaim.Attempt != 2 {
		t.Fatalf("replacement claim=%+v err=%v", newClaim, err)
	}
	newLease := execution.Lease{ExecutionID: newClaim.ID, WorkerID: newClaim.WorkerID, Fencing: newClaim.FencingToken, LeaseUntil: newClaim.LeaseUntil}
	if err := durable.Start(ctx, newLease); err != nil {
		t.Fatalf("start replacement claim: %v", err)
	}
	resultJSON := []byte(`{"summary":"fenced worker completion","verified":true}`)
	evidence := []execution.Evidence{{Source: "phase10-integration", Description: "replacement worker evidence", ObservedAt: time.Now().UTC()}}
	if err := durable.Complete(ctx, oldLease, resultJSON, evidence, true); !errors.Is(err, execution.ErrLeaseLost) {
		t.Fatalf("stale worker completion error=%v", err)
	}
	if _, err := durable.SaveCheckpoint(ctx, oldLease, execution.Checkpoint{StepID: "stale-step", Progress: []byte(`{}`), SchemaVersion: 1}); !errors.Is(err, execution.ErrLeaseLost) {
		t.Fatalf("stale worker checkpoint error=%v", err)
	}
	if err := durable.Complete(ctx, newLease, resultJSON, evidence, true); err != nil {
		t.Fatalf("replacement worker complete: %v", err)
	}
	completed, err := durable.Get(ctx, claimed.ID)
	completedTask, taskErr := taskRepo.Get(ctx, runtimeTask.ID)
	if err != nil || completed.Status != execution.StatusSucceeded || taskErr != nil || completedTask.Status != task.Completed {
		t.Fatalf("fenced completion run=%+v task=%+v runErr=%v taskErr=%v", completed, completedTask, err, taskErr)
	}
	var transitions int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM execution_state_history WHERE execution_run_id=$1", claimed.ID).Scan(&transitions); err != nil || transitions < 5 {
		t.Fatalf("execution state history transitions=%d err=%v", transitions, err)
	}

	unsafeTask, err := taskRepo.Create(ctx, task.Task{GoalID: goalID, Title: uniqueLabel("unsafe-expiry"), Priority: 2, Status: task.Ready})
	if err != nil {
		t.Fatalf("create uncertain outcome task: %v", err)
	}
	*taskIDs = append(*taskIDs, unsafeTask.ID)
	unsafeRequest := request
	unsafeRequest.TaskID, unsafeRequest.IdempotencyScope, unsafeRequest.IdempotencyKey = unsafeTask.ID, unsafeTask.ID, "attempt-0"
	unsafeRequest.RetrySafe = false
	unsafeRequest.InputHash = fmt.Sprintf("%x", sha256.Sum256(unsafeRequest.Payload))
	unsafeRun, _, err := durable.Enqueue(ctx, unsafeRequest)
	if err != nil {
		t.Fatalf("enqueue non-retry-safe execution: %v", err)
	}
	unsafeClaim, err := durable.Claim(ctx, "unsafe-worker", 3*time.Second)
	if err != nil || unsafeClaim.ID != unsafeRun.ID {
		t.Fatalf("claim non-retry-safe run=%+v err=%v", unsafeClaim, err)
	}
	unsafeLease := execution.Lease{ExecutionID: unsafeClaim.ID, WorkerID: unsafeClaim.WorkerID, Fencing: unsafeClaim.FencingToken, LeaseUntil: unsafeClaim.LeaseUntil}
	if err := durable.Start(ctx, unsafeLease); err != nil {
		t.Fatalf("start non-retry-safe run: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE execution_runs SET lease_until=now()-interval '1 second' WHERE id=$1", unsafeRun.ID); err != nil {
		t.Fatalf("expire non-retry-safe lease: %v", err)
	}
	if recovered, err := durable.RecoverExpired(ctx, 10, 0); err != nil || recovered != 1 {
		t.Fatalf("recover uncertain execution count=%d err=%v", recovered, err)
	}
	unsafeState, err := durable.Get(ctx, unsafeRun.ID)
	if err != nil || unsafeState.Status != execution.StatusUnknown {
		t.Fatalf("uncertain external outcome was retried rather than marked unknown: %+v err=%v", unsafeState, err)
	}
	if _, err := durable.Claim(ctx, "must-not-repeat", 3*time.Second); !errors.Is(err, execution.ErrNoExecution) {
		t.Fatalf("unsafe unknown execution was made claimable: %v", err)
	}
	if err := durable.RequestCancel(ctx, unsafeRun.ID); err == nil {
		t.Fatal("terminal unknown execution should not accept a cancellation rewrite")
	}
}

func TestPostgresPersistenceAcrossServerRestart(t *testing.T) {
	mode := os.Getenv("PAIOS_TEST_RESTART_MODE")
	if mode == "" {
		t.Skip("set PAIOS_TEST_RESTART_MODE=prepare or verify through make test-integration")
	}
	pool, repository := integrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply migrations for restart test: %v", err)
	}

	service := startCoreService(t, ctx, os.Getenv("PAIOS_TEST_DATABASE_URL"))
	defer service.stop(t)

	const goalTitle = "paios-restart-fixture-goal"
	if mode == "prepare" {
		var existing int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM goals WHERE title=$1", goalTitle).Scan(&existing); err != nil {
			t.Fatal(err)
		}
		if existing != 0 {
			t.Fatal("restart fixture already exists; run make test-integration to recreate the disposable test database")
		}
		var createdGoal goal.Goal
		service.request(t, http.MethodPost, "/api/v1/goals", map[string]any{"title": goalTitle, "priority": 4, "success_criteria": []string{"survives database and Go service restart"}}, http.StatusCreated, &createdGoal)
		var createdTask task.Task
		service.request(t, http.MethodPost, "/api/v1/tasks", map[string]any{"goal_id": createdGoal.ID, "title": "paios-restart-fixture-task", "priority": 3}, http.StatusCreated, &createdTask)

		tasks := repository.Tasks()
		if _, err := tasks.Transition(ctx, createdTask.ID, task.Ready); err != nil {
			t.Fatalf("prepare task ready state: %v", err)
		}
		if _, err := tasks.Transition(ctx, createdTask.ID, task.Running); err != nil {
			t.Fatalf("prepare task running state: %v", err)
		}
		executionStore := store.NewExecutionStore(pool)
		if _, err := executionStore.Save(ctx, supervisor.Execution{
			TaskID: createdTask.ID, AgentID: "restart-test", Summary: "persisted before PostgreSQL restart", Outcome: agentruntime.OutcomeSucceeded,
			Evidence: []supervisor.Evidence{{TaskID: createdTask.ID, Source: "restart-test", Description: "execution evidence survived PostgreSQL restart", ObservedAt: time.Now().UTC()}},
			CompletedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("persist restart execution: %v", err)
		}
		if _, err := tasks.Transition(ctx, createdTask.ID, task.Completed); err != nil {
			t.Fatalf("complete restart task: %v", err)
		}
		service.request(t, http.MethodPost, "/api/v1/goals/"+createdGoal.ID+"/activate", nil, http.StatusOK, nil)
		service.request(t, http.MethodPost, "/api/v1/goals/"+createdGoal.ID+"/complete", map[string]any{"evidence": []map[string]any{{"source": "restart-test", "description": "goal completion evidence", "reference": "test"}}}, http.StatusOK, &createdGoal)

		var createdApproval approval.Request
		service.request(t, http.MethodPost, "/api/v1/approvals", map[string]any{"task_id": createdTask.ID, "tool_id": "restart-test-tool", "action": "persist", "parameters": map[string]any{"key": "stable"}, "risk_level": 3}, http.StatusCreated, &createdApproval)
		service.request(t, http.MethodPost, "/api/v1/approvals/"+createdApproval.ID+"/approve", map[string]any{}, http.StatusOK, &createdApproval)
		var createdMemory memory.Memory
		service.request(t, http.MethodPost, "/api/v1/memories", map[string]any{"category": "fact", "content": "paios-restart-fixture-memory persisted cobalt", "source": "restart-test", "confidence": 0.9}, http.StatusCreated, &createdMemory)
		service.request(t, http.MethodPost, "/api/v1/knowledge/documents", map[string]any{"title": "paios-restart-fixture-document", "source": "restart-test", "content": "restart fixture knowledge durable cobalt marker"}, http.StatusCreated, nil)
		service.request(t, http.MethodPost, "/api/v1/nodes/register", map[string]any{"identity": "paios-restart-fixture-node", "name": "Restart Fixture Node", "type": "worker", "capabilities": []string{"restart-test"}}, http.StatusCreated, nil)

		if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_records WHERE resource_id=$1", createdGoal.ID).Scan(&existing); err != nil || existing == 0 {
			t.Fatalf("prepared audit rows=%d err=%v", existing, err)
		}
		service.stop(t)
		recoveryGoal, err := repository.Create(ctx, goal.Goal{OwnerID: testOwnerID, Title: "paios-restart-fixture-recovery-goal", Priority: 3, Status: goal.Active})
		if err != nil {
			t.Fatalf("create recovery Goal: %v", err)
		}
		recoveryTask, err := repository.Tasks().Create(ctx, task.Task{GoalID: recoveryGoal.ID, Title: "paios-restart-fixture-recovery-task", Priority: 3, Status: task.Ready})
		if err != nil {
			t.Fatalf("create recovery Task: %v", err)
		}
		if _, err := repository.Tasks().Transition(ctx, recoveryTask.ID, task.Running); err != nil {
			t.Fatalf("mark recovery Task running before simulated crash: %v", err)
		}
		payload, err := json.Marshal(agentruntime.Request{TaskID: recoveryTask.ID, GoalID: recoveryGoal.ID, Title: recoveryTask.Title, Description: "recovered by startup worker", MaxSteps: 4, MaxModelCalls: 1, MaxToolCalls: 1, MaxTokens: 1000})
		if err != nil {
			t.Fatal(err)
		}
		inputHash := fmt.Sprintf("%x", sha256.Sum256(payload))
		var recoveryRunID string
		err = pool.QueryRow(ctx, `INSERT INTO execution_runs(task_id,status,idempotency_key,input_hash,payload,agent_id,worker_id,claimed_at,lease_until,heartbeat_at,fencing_token,attempt,max_attempts,retry_safe,idempotent,state_reason,state_changed_at)
			VALUES($1,'claimed',$2,$3,$4,'planning','simulated-crashed-worker',now()-interval '1 minute',now()-interval '30 seconds',now()-interval '30 seconds',5,1,3,true,true,'worker crashed before executor start',now()-interval '30 seconds') RETURNING id::text`, recoveryTask.ID, "restart-recovery:"+recoveryTask.ID, inputHash, payload).Scan(&recoveryRunID)
		if err != nil {
			t.Fatalf("persist expired in-flight execution before restart: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO execution_idempotency(scope,idempotency_key,input_hash,execution_run_id) VALUES($1,'attempt-0',$2,$3)`, recoveryTask.ID, inputHash, recoveryRunID); err != nil {
			t.Fatalf("persist recovery execution idempotency key: %v", err)
		}
		var preparedCount int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM tasks WHERE title='paios-restart-fixture-recovery-task'").Scan(&preparedCount); err != nil || preparedCount != 1 {
			t.Fatalf("recovery fixture task count=%d err=%v", preparedCount, err)
		}
		for _, transition := range []struct{ from, to string }{{"queued", "claimed"}} {
			if _, err := pool.Exec(ctx, `INSERT INTO execution_state_history(execution_run_id,from_status,to_status,worker_id,fencing_token,reason) VALUES($1,$2,$3,'simulated-crashed-worker',5,'restart fixture')`, recoveryRunID, transition.from, transition.to); err != nil {
				t.Fatalf("record recovery fixture state: %v", err)
			}
		}
		t.Log("prepared Goal, Task, Execution, Evidence, Approval, Memory, Knowledge, Node, Audit, Outbox, plus an expired in-flight run; Core process stopped before PostgreSQL restart")
		return
	}
	if mode != "verify" {
		t.Fatalf("unsupported PAIOS_TEST_RESTART_MODE %q", mode)
	}
	var recoveryRunID, recoveryTaskID string
	if err := pool.QueryRow(ctx, `SELECT e.id::text,e.task_id::text FROM execution_runs e JOIN tasks t ON t.id=e.task_id WHERE t.title='paios-restart-fixture-recovery-task' ORDER BY e.started_at DESC LIMIT 1`).Scan(&recoveryRunID, &recoveryTaskID); err != nil {
		var taskCount int
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM tasks WHERE title='paios-restart-fixture-recovery-task'").Scan(&taskCount)
		t.Fatalf("find expired in-flight run after restart (fixture tasks=%d): %v", taskCount, err)
	}
	waitForRecoveredTerminalRun(t, ctx, pool, recoveryRunID, service)
	recoveryRun, err := store.NewDurableExecutionStore(pool).Get(ctx, recoveryRunID)
	if err != nil || recoveryRun.FencingToken <= 5 || recoveryRun.Status != execution.StatusFailed {
		t.Fatalf("startup scanner/worker did not fence and resume expired run: %+v err=%v", recoveryRun, err)
	}
	recoveryTask, err := repository.Tasks().Get(ctx, recoveryTaskID)
	if err != nil || recoveryTask.Status != task.Failed {
		t.Fatalf("startup recovered Task=%+v err=%v", recoveryTask, err)
	}

	var goals struct {
		Items []goal.Goal `json:"items"`
	}
	service.request(t, http.MethodGet, "/api/v1/goals", nil, http.StatusOK, &goals)
	var goalID string
	for _, item := range goals.Items {
		if item.Title == goalTitle {
			goalID = item.ID
			break
		}
	}
	var fixtureGoal goal.Goal
	if goalID != "" {
		service.request(t, http.MethodGet, "/api/v1/goals/"+goalID, nil, http.StatusOK, &fixtureGoal)
	}
	if fixtureGoal.ID == "" || fixtureGoal.Status != goal.Completed || len(fixtureGoal.Evidence) != 1 {
		t.Fatalf("Goal did not survive service/database restart: %+v", fixtureGoal)
	}
	var listedTasks struct {
		Items []task.Task `json:"items"`
	}
	service.request(t, http.MethodGet, "/api/v1/tasks?goal_id="+fixtureGoal.ID, nil, http.StatusOK, &listedTasks)
	if len(listedTasks.Items) != 1 || listedTasks.Items[0].Status != task.Completed {
		t.Fatalf("Task did not survive restart: %+v", listedTasks.Items)
	}
	executions, err := store.NewExecutionStore(pool).ListByGoal(ctx, fixtureGoal.ID)
	if err != nil || len(executions) != 1 || len(executions[0].Evidence) != 1 {
		t.Fatalf("Execution/Evidence did not survive restart: items=%+v err=%v", executions, err)
	}
	var approvals struct {
		Items []approval.Request `json:"items"`
	}
	service.request(t, http.MethodGet, "/api/v1/approvals", nil, http.StatusOK, &approvals)
	var approvalID string
	if err := pool.QueryRow(ctx, "SELECT id::text FROM approvals WHERE task_id=$1 AND tool_id='restart-test-tool'", listedTasks.Items[0].ID).Scan(&approvalID); err != nil {
		t.Fatalf("find persisted approval: %v", err)
	}
	var persistedApproval approval.Request
	service.request(t, http.MethodGet, "/api/v1/approvals/"+approvalID, nil, http.StatusOK, &persistedApproval)
	if persistedApproval.Status != approval.Approved {
		t.Fatalf("Approval did not survive restart: %+v", persistedApproval)
	}
	var memories struct {
		Items []memory.Memory `json:"items"`
	}
	service.request(t, http.MethodPost, "/api/v1/memories/search", map[string]any{"query": "restart-fixture-memory"}, http.StatusOK, &memories)
	if len(memories.Items) != 1 {
		t.Fatalf("Memory did not survive restart: %+v", memories.Items)
	}
	var citations struct {
		Items []knowledge.Citation `json:"items"`
	}
	service.request(t, http.MethodPost, "/api/v1/knowledge/search", map[string]any{"query": "durable cobalt marker"}, http.StatusOK, &citations)
	if len(citations.Items) != 1 || citations.Items[0].Title != "paios-restart-fixture-document" {
		t.Fatalf("Knowledge did not survive restart: %+v", citations.Items)
	}
	var nodes struct {
		Items []node.Node `json:"items"`
	}
	service.request(t, http.MethodGet, "/api/v1/nodes", nil, http.StatusOK, &nodes)
	foundNode := false
	var restartNodeID string
	for _, item := range nodes.Items {
		if item.Identity == "paios-restart-fixture-node" && item.Status == node.Offline {
			foundNode = true
			restartNodeID = item.ID
		}
	}
	if !foundNode {
		t.Fatal("Node did not survive restart")
	}
	var documents []knowledge.Document
	documents, err = repository.Knowledge().List(ctx, testOwnerID, 100)
	if err != nil {
		t.Fatalf("list persisted Knowledge documents: %v", err)
	}
	var restartDocumentID string
	for _, item := range documents {
		if item.Title == "paios-restart-fixture-document" {
			restartDocumentID = item.ID
		}
	}
	if restartDocumentID == "" {
		t.Fatal("Knowledge document metadata did not survive restart")
	}
	var auditCount, outboxCount int
	for _, id := range []string{fixtureGoal.ID, listedTasks.Items[0].ID, approvalID} {
		var auditsForResource, eventsForAggregate int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_records WHERE resource_id=$1", id).Scan(&auditsForResource); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM event_outbox WHERE aggregate_id=$1", id).Scan(&eventsForAggregate); err != nil {
			t.Fatal(err)
		}
		auditCount += auditsForResource
		outboxCount += eventsForAggregate
	}
	if auditCount == 0 || outboxCount == 0 {
		t.Fatalf("Audit/Outbox did not survive restart: audit=%d outbox=%d", auditCount, outboxCount)
	}
	cleanupIntegrationRows(t, context.Background(), pool, []string{fixtureGoal.ID, recoveryTask.GoalID}, []string{listedTasks.Items[0].ID, recoveryTask.ID}, []string{memories.Items[0].ID}, []string{restartDocumentID}, []string{restartNodeID}, []string{approvalID})
	t.Log("fresh Personal AI OS process read persisted Goal/Task/Execution/Evidence/Approval/Memory/Knowledge/Node/Audit/Outbox after PostgreSQL container restart")
}

func TestPostgresDurableWorkerPoolAndCancellationOutcome(t *testing.T) {
	pool, repository := integrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply migrations for worker runtime test: %v", err)
	}
	goalItem, err := repository.Create(ctx, goal.Goal{OwnerID: testOwnerID, Title: uniqueLabel("worker-runtime-goal"), Priority: 3, Status: goal.Active})
	if err != nil {
		t.Fatalf("create worker runtime Goal: %v", err)
	}
	goalIDs := []string{goalItem.ID}
	var taskIDs []string
	t.Cleanup(func() { cleanupIntegrationRows(t, context.Background(), pool, goalIDs, taskIDs, nil, nil, nil, nil) })
	taskRepo := repository.Tasks()
	durable := store.NewDurableExecutionStore(pool)

	completedTask, err := taskRepo.Create(ctx, task.Task{GoalID: goalItem.ID, Title: uniqueLabel("worker-success"), Priority: 3, Status: task.Ready})
	if err != nil {
		t.Fatalf("create worker task: %v", err)
	}
	taskIDs = append(taskIDs, completedTask.ID)
	successRequest := newExecutionEnqueueRequest(completedTask.ID, "test-worker", "attempt-0", []byte(`{"work":"complete"}`), 1, true)
	queued, _, err := durable.Enqueue(ctx, successRequest)
	if err != nil {
		t.Fatalf("enqueue Worker Pool success run: %v", err)
	}
	poolCtx, stopPool := context.WithCancel(context.Background())
	poolService, err := execution.NewWorkerPool(durable, successfulTestExecutor{}, execution.WorkerConfig{
		WorkerID: uniqueLabel("pool"), Concurrency: 2, LeaseDuration: time.Second, ExecutionTimeout: 5 * time.Second,
		HeartbeatInterval: 100 * time.Millisecond, PollInterval: 10 * time.Millisecond, RetryBaseDelay: 10 * time.Millisecond,
		RecoveryInterval: 100 * time.Millisecond, RecoveryBatch: 10, ShutdownGrace: time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("create durable Worker Pool: %v", err)
	}
	poolDone := make(chan error, 1)
	go func() { poolDone <- poolService.Run(poolCtx) }()
	successfulRun := waitForRun(t, ctx, durable, queued.ID, execution.StatusSucceeded)
	completedTask, err = taskRepo.Get(ctx, completedTask.ID)
	if err != nil || completedTask.Status != task.Completed {
		t.Fatalf("Worker Pool task=%+v err=%v", completedTask, err)
	}
	checkpoint, err := durable.LatestCheckpoint(ctx, queued.ID)
	if err != nil || checkpoint.Sequence != 1 || checkpoint.StepID != "prepare" {
		t.Fatalf("Worker Pool checkpoint=%+v err=%v", checkpoint, err)
	}
	if successfulRun.WorkerID != "" || successfulRun.FencingToken != 1 {
		t.Fatalf("completed worker lease was not cleared or fenced: %+v", successfulRun)
	}

	cancelTask, err := taskRepo.Create(ctx, task.Task{GoalID: goalItem.ID, Title: uniqueLabel("worker-cancel-uncertain"), Priority: 2, Status: task.Ready})
	if err != nil {
		t.Fatalf("create cancellation task: %v", err)
	}
	taskIDs = append(taskIDs, cancelTask.ID)
	cancelRequest := newExecutionEnqueueRequest(cancelTask.ID, "blocking-test-worker", "attempt-0", []byte(`{"work":"wait"}`), 3, false)
	cancelRun, _, err := durable.Enqueue(ctx, cancelRequest)
	if err != nil {
		t.Fatalf("enqueue cancellation test run: %v", err)
	}
	blockingExecutor := blockingTestExecutor{started: make(chan struct{}, 1)}
	stopPool()
	select {
	case <-poolDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Worker Pool did not stop after context cancellation")
	}
	poolCtx, stopPool = context.WithCancel(context.Background())
	poolService, err = execution.NewWorkerPool(durable, blockingExecutor, execution.WorkerConfig{
		WorkerID: uniqueLabel("cancel-pool"), Concurrency: 1, LeaseDuration: time.Second, ExecutionTimeout: 5 * time.Second,
		HeartbeatInterval: 100 * time.Millisecond, PollInterval: 10 * time.Millisecond, RetryBaseDelay: 10 * time.Millisecond,
		RecoveryInterval: 100 * time.Millisecond, RecoveryBatch: 10, ShutdownGrace: time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("create cancellation Worker Pool: %v", err)
	}
	poolDone = make(chan error, 1)
	go func() { poolDone <- poolService.Run(poolCtx) }()
	select {
	case <-blockingExecutor.started:
	case <-ctx.Done():
		t.Fatalf("blocking execution never started: %v", ctx.Err())
	}
	if err := durable.RequestCancel(ctx, cancelRun.ID); err != nil {
		t.Fatalf("request execution cancellation: %v", err)
	}
	unknownRun := waitForRun(t, ctx, durable, cancelRun.ID, execution.StatusUnknown)
	if unknownRun.FailureClass != execution.FailureUncertain {
		t.Fatalf("cancelled in-flight external operation was not classified uncertain: %+v", unknownRun)
	}
	stopPool()
	select {
	case <-poolDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation Worker Pool did not stop")
	}
}

type successfulTestExecutor struct{}

func (successfulTestExecutor) Execute(ctx context.Context, run execution.Run, checkpoint *execution.Checkpoint, progress execution.Progress) (execution.ExecutionResult, error) {
	if _, err := progress.ClaimStep(ctx, "prepare", "prepare:"+run.TaskID, fmt.Sprintf("%x", sha256.Sum256([]byte(run.TaskID)))); err != nil {
		return execution.ExecutionResult{}, err
	}
	if err := progress.SaveCheckpoint(ctx, execution.Checkpoint{StepID: "prepare", Progress: []byte(`{"ready":true}`), CompletedSteps: []string{"prepare"}, SchemaVersion: 1}); err != nil {
		return execution.ExecutionResult{}, err
	}
	result, err := json.Marshal(supervisor.Execution{TaskID: run.TaskID, AgentID: run.AgentID, Summary: "worker complete", Outcome: agentruntime.OutcomeSucceeded, Verified: true, Evidence: []supervisor.Evidence{{TaskID: run.TaskID, Source: "worker-test", Description: "completed by durable worker", ObservedAt: time.Now().UTC()}}, CompletedAt: time.Now().UTC()})
	if err != nil {
		return execution.ExecutionResult{}, err
	}
	return execution.ExecutionResult{Result: result, Evidence: []execution.Evidence{{Source: "worker-test", Description: "completed by durable worker", ObservedAt: time.Now().UTC()}}, VerifiedResult: true}, nil
}

type blockingTestExecutor struct{ started chan struct{} }

func (executor blockingTestExecutor) Execute(ctx context.Context, _ execution.Run, _ *execution.Checkpoint, _ execution.Progress) (execution.ExecutionResult, error) {
	executor.started <- struct{}{}
	<-ctx.Done()
	return execution.ExecutionResult{}, ctx.Err()
}

func waitForRun(t *testing.T, ctx context.Context, repository execution.Repository, runID string, expected execution.RunStatus) execution.Run {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		run, err := repository.Get(ctx, runID)
		if err != nil {
			t.Fatalf("poll execution %s: %v", runID, err)
		}
		if run.Status == expected {
			return run
		}
		select {
		case <-ctx.Done():
			t.Fatalf("execution %s did not reach %s; current=%s reason=%s", runID, expected, run.Status, run.FailureReason)
		case <-ticker.C:
		}
	}
}

func waitForRecoveredTerminalRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID string, service *coreService) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var recoveryRows int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM execution_state_history WHERE execution_run_id=$1 AND reason LIKE 'lease expired%'", runID).Scan(&recoveryRows); err != nil {
			t.Fatalf("query startup recovery history: %v", err)
		}
		var status string
		if err := pool.QueryRow(ctx, "SELECT status FROM execution_runs WHERE id=$1", runID).Scan(&status); err != nil {
			t.Fatalf("query recovered execution status: %v", err)
		}
		if recoveryRows > 0 && (status == string(execution.StatusFailed) || status == string(execution.StatusUnknown) || status == string(execution.StatusSucceeded)) {
			return
		}
		select {
		case <-ctx.Done():
			var status, reason, taskStatus string
			var token int64
			_ = pool.QueryRow(context.Background(), "SELECT status,state_reason,fencing_token FROM execution_runs WHERE id=$1", runID).Scan(&status, &reason, &token)
			_ = pool.QueryRow(context.Background(), "SELECT status FROM tasks WHERE id=(SELECT task_id FROM execution_runs WHERE id=$1)", runID).Scan(&taskStatus)
			var history string
			_ = pool.QueryRow(context.Background(), "SELECT COALESCE(string_agg(from_status||'->'||to_status||':'||reason, ';' ORDER BY occurred_at),'') FROM execution_state_history WHERE execution_run_id=$1", runID).Scan(&history)
			t.Fatalf("startup recovery scanner did not process expired run %s; status=%s token=%d reason=%s task=%s history=%s core_logs=%s", runID, status, token, reason, taskStatus, history, service.logs.String())
		case <-ticker.C:
		}
	}
}

func cleanupIntegrationRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, goalIDs, taskIDs, memoryIDs, documentIDs, nodeIDs, approvalIDs []string) {
	t.Helper()
	allIDs := append([]string{}, goalIDs...)
	allIDs = append(allIDs, taskIDs...)
	allIDs = append(allIDs, memoryIDs...)
	allIDs = append(allIDs, documentIDs...)
	allIDs = append(allIDs, nodeIDs...)
	allIDs = append(allIDs, approvalIDs...)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin integration fixture cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if len(allIDs) > 0 {
		if _, err := tx.Exec(ctx, "DELETE FROM audit_records WHERE resource_id=ANY($1::text[])", allIDs); err != nil {
			t.Errorf("clean integration audit fixtures: %v", err)
			return
		}
		if _, err := tx.Exec(ctx, "DELETE FROM event_outbox WHERE aggregate_id=ANY($1::uuid[])", allIDs); err != nil {
			t.Errorf("clean integration outbox fixtures: %v", err)
			return
		}
	}
	if len(approvalIDs) > 0 {
		if _, err := tx.Exec(ctx, "DELETE FROM approvals WHERE id::text=ANY($1::text[])", approvalIDs); err != nil {
			t.Errorf("clean integration approvals: %v", err)
			return
		}
	}
	if len(taskIDs) > 0 {
		if _, err := tx.Exec(ctx, "DELETE FROM task_dependencies WHERE task_id::text=ANY($1::text[]) OR depends_on_task_id::text=ANY($1::text[])", taskIDs); err != nil {
			t.Errorf("clean integration task dependencies: %v", err)
			return
		}
		if _, err := tx.Exec(ctx, "DELETE FROM execution_runs WHERE task_id::text=ANY($1::text[])", taskIDs); err != nil {
			t.Errorf("clean integration executions: %v", err)
			return
		}
		if _, err := tx.Exec(ctx, "DELETE FROM tasks WHERE id::text=ANY($1::text[])", taskIDs); err != nil {
			t.Errorf("clean integration tasks: %v", err)
			return
		}
	}
	if len(goalIDs) > 0 {
		if _, err := tx.Exec(ctx, "DELETE FROM goal_evidence WHERE goal_id::text=ANY($1::text[])", goalIDs); err != nil {
			t.Errorf("clean integration goal evidence: %v", err)
			return
		}
		if _, err := tx.Exec(ctx, "DELETE FROM model_usage WHERE goal_id::text=ANY($1::text[])", goalIDs); err != nil {
			t.Errorf("clean integration model usage: %v", err)
			return
		}
		if _, err := tx.Exec(ctx, "DELETE FROM goals WHERE id::text=ANY($1::text[])", goalIDs); err != nil {
			t.Errorf("clean integration goals: %v", err)
			return
		}
	}
	if len(memoryIDs) > 0 {
		if _, err := tx.Exec(ctx, "DELETE FROM memories WHERE id::text=ANY($1::text[])", memoryIDs); err != nil {
			t.Errorf("clean integration memories: %v", err)
			return
		}
	}
	if len(documentIDs) > 0 {
		if _, err := tx.Exec(ctx, "DELETE FROM knowledge_documents WHERE id::text=ANY($1::text[])", documentIDs); err != nil {
			t.Errorf("clean integration documents: %v", err)
			return
		}
	}
	if len(nodeIDs) > 0 {
		if _, err := tx.Exec(ctx, "DELETE FROM nodes WHERE id::text=ANY($1::text[])", nodeIDs); err != nil {
			t.Errorf("clean integration nodes: %v", err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit integration fixture cleanup: %v", err)
	}
}

func withAuditFailure(t *testing.T, ctx context.Context, pool *pgxpool.Pool, action string, run func()) {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	functionName := "paios_test_reject_audit_" + suffix
	triggerName := "paios_test_reject_audit_trigger_" + suffix
	functionSQL := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action = '%s' THEN RAISE EXCEPTION 'injected integration audit failure' USING ERRCODE='23514'; END IF; RETURN NEW; END $$`, functionName, action)
	if _, err := pool.Exec(ctx, functionSQL); err != nil {
		t.Fatalf("create isolated audit-failure function: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION %s()", triggerName, functionName)); err != nil {
		_, _ = pool.Exec(context.Background(), "DROP FUNCTION "+functionName+"()")
		t.Fatalf("create isolated audit-failure trigger: %v", err)
	}
	defer func() {
		if _, err := pool.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON audit_records", triggerName)); err != nil {
			t.Errorf("drop isolated audit-failure trigger: %v", err)
		}
		if _, err := pool.Exec(context.Background(), "DROP FUNCTION IF EXISTS "+functionName+"()"); err != nil {
			t.Errorf("drop isolated audit-failure function: %v", err)
		}
	}()
	run()
}

func assertCheckpointFailureRollsBack(t *testing.T, ctx context.Context, pool *pgxpool.Pool, durable *store.DurableExecutionStore, lease execution.Lease, runID string) {
	t.Helper()
	var before int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM execution_checkpoints WHERE execution_run_id=$1", runID).Scan(&before); err != nil {
		t.Fatalf("count checkpoints before failure injection: %v", err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	functionName := "paios_test_reject_checkpoint_" + suffix
	triggerName := "paios_test_reject_checkpoint_trigger_" + suffix
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected checkpoint database failure' USING ERRCODE='23514'; END $$`, functionName)); err != nil {
		t.Fatalf("create checkpoint failure function: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON execution_checkpoints FOR EACH ROW EXECUTE FUNCTION %s()", triggerName, functionName)); err != nil {
		_, _ = pool.Exec(context.Background(), "DROP FUNCTION "+functionName+"()")
		t.Fatalf("create checkpoint failure trigger: %v", err)
	}
	if _, err := durable.SaveCheckpoint(ctx, lease, execution.Checkpoint{StepID: "failed-db-step", Progress: []byte(`{"must":"rollback"}`), SchemaVersion: 1}); err == nil {
		t.Fatal("expected injected database failure while saving checkpoint")
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("DROP TRIGGER %s ON execution_checkpoints", triggerName)); err != nil {
		t.Fatalf("drop checkpoint failure trigger: %v", err)
	}
	if _, err := pool.Exec(ctx, "DROP FUNCTION "+functionName+"()"); err != nil {
		t.Fatalf("drop checkpoint failure function: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM execution_checkpoints WHERE execution_run_id=$1", runID).Scan(&count); err != nil || count != before {
		t.Fatalf("failed checkpoint transaction changed row count: before=%d after=%d err=%v", before, count, err)
	}
}

func countForGoal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table, column, goalID string) int {
	t.Helper()
	query := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s=$1", table, column)
	var count int
	if err := pool.QueryRow(ctx, query, goalID).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func assertCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, argument string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, query, argument).Scan(&got); err != nil || got != want {
		t.Fatalf("query count=%d err=%v, want %d", got, err, want)
	}
}

func assertZero(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, argument string) {
	t.Helper()
	assertCount(t, ctx, pool, query, argument, 0)
}

func assertPostgresCode(t *testing.T, err error, code, operation string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		t.Fatalf("%s error=%v, want PostgreSQL error %s", operation, err, code)
	}
	if postgresError.Code != code {
		t.Fatalf("%s SQLSTATE=%s, want %s (message %s)", operation, postgresError.Code, code, postgresError.Message)
	}
}

func uniqueLabel(prefix string) string { return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()) }

func newExecutionEnqueueRequest(taskID, agentID, key string, payload []byte, maxAttempts int, retrySafe bool) execution.EnqueueRequest {
	return execution.EnqueueRequest{TaskID: taskID, AgentID: agentID, IdempotencyScope: taskID, IdempotencyKey: key,
		InputHash: fmt.Sprintf("%x", sha256.Sum256(payload)), Payload: payload, MaxAttempts: maxAttempts, RetrySafe: retrySafe, Idempotent: true}
}

type countingTool struct{ calls int }

func (tool *countingTool) Execute(context.Context, map[string]any) (any, error) {
	tool.calls++
	return map[string]any{"ok": true}, nil
}

type coreService struct {
	baseURL string
	command *exec.Cmd
	wait    chan error
	logs    *strings.Builder
	stopOnce sync.Once
}

func startCoreService(t *testing.T, ctx context.Context, databaseURL string) *coreService {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve API port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../.."))
	binary := filepath.Join(root, "bin", "paios")
	command := exec.CommandContext(ctx, binary)
	command.Dir = root
	command.Env = serviceEnvironment(os.Environ(), map[string]string{
		"APP_ENV": "test", "APP_HOST": "127.0.0.1", "APP_PORT": fmt.Sprint(port),
		"DATABASE_URL": databaseURL, "PAIOS_API_KEY": "", "MODEL_PROVIDER": "mock", "LOG_LEVEL": "error",
		"MAX_EXECUTION_SECONDS": "30", "PAIOS_WORKER_ID": "integration", "PAIOS_WORKER_COUNT": "1",
		"PAIOS_WORKER_LEASE_SECONDS": "4", "PAIOS_WORKER_HEARTBEAT_SECONDS": "1", "PAIOS_WORKER_POLL_MILLISECONDS": "25",
		"PAIOS_WORKER_RECOVERY_SECONDS": "1", "SHUTDOWN_GRACE_SECONDS": "5",
	})
	logs := &strings.Builder{}
	command.Stdout = logs
	command.Stderr = logs
	if err := command.Start(); err != nil {
		t.Fatalf("start Personal AI OS service: %v", err)
	}
	service := &coreService{baseURL: fmt.Sprintf("http://127.0.0.1:%d", port), command: command, wait: make(chan error, 1), logs: logs}
	go func() { service.wait <- command.Wait() }()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	client := &http.Client{Timeout: time.Second}
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, service.baseURL+"/health", nil)
		response, requestErr := client.Do(request)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return service
			}
		}
		select {
		case processErr := <-service.wait:
			t.Fatalf("Personal AI OS exited before becoming healthy: %v\n%s", processErr, logs.String())
		case <-deadline.C:
			_ = command.Process.Kill()
			<-service.wait
			t.Fatalf("Personal AI OS did not become healthy\n%s", logs.String())
		case <-ctx.Done():
			_ = command.Process.Kill()
			<-service.wait
			t.Fatalf("waiting for Personal AI OS health: %v\n%s", ctx.Err(), logs.String())
		case <-ticker.C:
		}
	}
}

func (service *coreService) request(t *testing.T, method, path string, input any, expectedStatus int, output any) {
	t.Helper()
	body := strings.NewReader("")
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatalf("encode %s request: %v", path, err)
		}
		body = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequest(method, service.baseURL+path, body)
	if err != nil {
		t.Fatalf("create %s request: %v", path, err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("request %s: %v", path, err)
	}
	defer response.Body.Close()
	responseBytes, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s response: %v", path, err)
	}
	responseBody := string(responseBytes)
	if response.StatusCode != expectedStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s service_logs=%s", method, path, response.StatusCode, expectedStatus, responseBody, service.logs.String())
	}
	if output != nil && len(responseBody) > 0 {
		if err := json.Unmarshal(responseBytes, output); err != nil {
			t.Fatalf("decode %s response %q: %v", path, responseBody, err)
		}
	}
}

func (service *coreService) stop(t *testing.T) {
	t.Helper()
	service.stopOnce.Do(func() {
		select {
		case err := <-service.wait:
			if err != nil {
				t.Errorf("Personal AI OS exited before shutdown: %v\n%s", err, service.logs.String())
			}
			return
		default:
		}
		if err := service.command.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("signal Personal AI OS shutdown: %v", err)
		}
		select {
		case err := <-service.wait:
			if err != nil {
				t.Errorf("Personal AI OS shutdown returned error: %v\n%s", err, service.logs.String())
			}
		case <-time.After(10 * time.Second):
			_ = service.command.Process.Kill()
			<-service.wait
			t.Errorf("Personal AI OS did not stop gracefully")
		}
	})
}

func serviceEnvironment(existing []string, overrides map[string]string) []string {
	result := make([]string, 0, len(existing)+len(overrides))
	for _, item := range existing {
		key, _, _ := strings.Cut(item, "=")
		if _, replaced := overrides[key]; !replaced {
			result = append(result, item)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}
