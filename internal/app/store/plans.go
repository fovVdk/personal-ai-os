// English: This adapter commits versioned Plan Proposals and their Task additions atomically.
// 中文：本适配器以原子事务提交版本化 Plan Proposal 及其新增 Task。
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/planner"
	"personal-ai-os/internal/task"
)

type VersionedPlanStore struct{ pool *pgxpool.Pool }

func NewVersionedPlanStore(pool *pgxpool.Pool) *VersionedPlanStore {
	return &VersionedPlanStore{pool: pool}
}

func (s *VersionedPlanStore) CreateProposal(ctx context.Context, proposal planner.PlanProposal) (planner.PlanProposal, error) {
	fingerprint := proposal.Fingerprint
	if fingerprint == "" {
		var err error
		fingerprint, err = proposal.ComputeFingerprint()
		if err != nil {
			return planner.PlanProposal{}, err
		}
	}
	payload, err := json.Marshal(proposal)
	if err != nil {
		return planner.PlanProposal{}, fmt.Errorf("encode plan proposal: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return planner.PlanProposal{}, fmt.Errorf("begin plan proposal transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var currentVersion int
	if err := tx.QueryRow(ctx, "SELECT COALESCE(max(version),0) FROM plan_versions WHERE goal_id=$1", proposal.GoalID).Scan(&currentVersion); err != nil {
		return planner.PlanProposal{}, fmt.Errorf("read current plan version: %w", err)
	}
	if currentVersion != proposal.BasePlanVersion || proposal.ProposedVersion != currentVersion+1 {
		return planner.PlanProposal{}, planner.ErrPlanConflict
	}
	if proposal.CreatedAt.IsZero() {
		proposal.CreatedAt = time.Now().UTC()
	}
	if proposal.ExpiresAt.IsZero() {
		proposal.ExpiresAt = proposal.CreatedAt.Add(30 * time.Minute)
	}
	payload, err = json.Marshal(proposal)
	if err != nil {
		return planner.PlanProposal{}, fmt.Errorf("encode normalized plan proposal: %w", err)
	}
	err = tx.QueryRow(ctx, `INSERT INTO plan_proposals(goal_id,base_version,proposed_version,status,trigger_type,reason,evaluation_id,fingerprint,payload,expires_at)
		VALUES($1,$2,$3,'proposed',$4,$5,NULLIF($6,'')::uuid,$7,$8,$9)
		ON CONFLICT(goal_id,fingerprint) DO UPDATE SET fingerprint=EXCLUDED.fingerprint RETURNING id::text,status,created_at,expires_at`,
		proposal.GoalID, proposal.BasePlanVersion, proposal.ProposedVersion, proposal.Trigger, proposal.Reason, proposal.EvaluationID, fingerprint, payload, proposal.ExpiresAt.UTC()).Scan(&proposal.ID, &proposal.Status, &proposal.CreatedAt, &proposal.ExpiresAt)
	if err != nil {
		return planner.PlanProposal{}, fmt.Errorf("persist plan proposal: %w", err)
	}
	proposal.Fingerprint = fingerprint
	if err := recordMutation(ctx, tx, LocalOwnerID, "plan.proposed", "plan_proposals", proposal.ID, proposal); err != nil {
		return planner.PlanProposal{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return planner.PlanProposal{}, fmt.Errorf("commit plan proposal: %w", err)
	}
	return proposal, nil
}

func (s *VersionedPlanStore) AcceptProposal(ctx context.Context, goalID, proposalID string, maxTasks int) (planner.PlanVersion, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return planner.PlanVersion{}, fmt.Errorf("begin plan commit: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var proposal planner.PlanProposal
	var payload []byte
	var status string
	err = tx.QueryRow(ctx, `SELECT id::text,goal_id::text,base_version,proposed_version,status,trigger_type,reason,COALESCE(evaluation_id::text,''),fingerprint,payload,created_at,expires_at
		FROM plan_proposals WHERE id=$1 AND goal_id=$2 FOR UPDATE`, proposalID, goalID).Scan(&proposal.ID, &proposal.GoalID, &proposal.BasePlanVersion, &proposal.ProposedVersion, &status, &proposal.Trigger, &proposal.Reason, &proposal.EvaluationID, &proposal.Fingerprint, &payload, &proposal.CreatedAt, &proposal.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return planner.PlanVersion{}, planner.ErrProposalNotFound
	}
	if err != nil {
		return planner.PlanVersion{}, fmt.Errorf("load plan proposal: %w", err)
	}
	proposal.Status = planner.ProposalStatus(status)
	if proposal.Status != planner.ProposalProposed {
		if proposal.Status == planner.ProposalAccepted {
			current, currentErr := currentPlanInTx(ctx, tx, proposal.GoalID)
			if currentErr == nil && current.Version == proposal.ProposedVersion {
				if err := tx.Commit(ctx); err != nil {
					return planner.PlanVersion{}, fmt.Errorf("commit idempotent Plan Proposal read: %w", err)
				}
				return current, nil
			}
		}
		return planner.PlanVersion{}, fmt.Errorf("proposal is not pending (status %s)", status)
	}
	if !proposal.ExpiresAt.After(time.Now().UTC()) {
		if _, err := tx.Exec(ctx, "UPDATE plan_proposals SET status='expired',resolved_at=now() WHERE id=$1", proposal.ID); err != nil {
			return planner.PlanVersion{}, fmt.Errorf("expire plan proposal: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return planner.PlanVersion{}, err
		}
		return planner.PlanVersion{}, planner.ErrProposalExpired
	}
	proposalIDFromRow := proposal.ID
	if err := json.Unmarshal(payload, &proposal); err != nil {
		return planner.PlanVersion{}, fmt.Errorf("decode plan proposal: %w", err)
	}
	proposal.ID = proposalIDFromRow
	var goalStatus string
	var criteriaJSON []byte
	if err := tx.QueryRow(ctx, "SELECT status,structured_success_criteria FROM goals WHERE id=$1 FOR UPDATE", proposal.GoalID).Scan(&goalStatus, &criteriaJSON); err != nil {
		return planner.PlanVersion{}, fmt.Errorf("lock plan Goal: %w", err)
	}
	if goalStatus != "active" && goalStatus != "monitoring" {
		return planner.PlanVersion{}, fmt.Errorf("cannot commit a plan for goal status %q", goalStatus)
	}
	var currentVersion int
	if err := tx.QueryRow(ctx, "SELECT COALESCE(max(version),0) FROM plan_versions WHERE goal_id=$1", proposal.GoalID).Scan(&currentVersion); err != nil {
		return planner.PlanVersion{}, fmt.Errorf("read current Plan Version: %w", err)
	}
	if currentVersion != proposal.BasePlanVersion || proposal.ProposedVersion != currentVersion+1 {
		if _, err := tx.Exec(ctx, "UPDATE plan_proposals SET status='conflict',resolved_at=now() WHERE id=$1", proposal.ID); err != nil {
			return planner.PlanVersion{}, fmt.Errorf("record stale proposal conflict: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return planner.PlanVersion{}, err
		}
		return planner.PlanVersion{}, planner.ErrPlanConflict
	}
	if proposal.ProposedVersion > 10 {
		return planner.PlanVersion{}, fmt.Errorf("maximum Plan Version budget is exhausted")
	}
	var totalTasks int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM tasks WHERE goal_id=$1", proposal.GoalID).Scan(&totalTasks); err != nil {
		return planner.PlanVersion{}, fmt.Errorf("count existing Goal tasks: %w", err)
	}
	if maxTasks < 1 || totalTasks > maxTasks {
		return planner.PlanVersion{}, fmt.Errorf("Goal task budget is exhausted")
	}
	var criteria []goal.SuccessCriterion
	if err := json.Unmarshal(criteriaJSON, &criteria); err != nil {
		return planner.PlanVersion{}, fmt.Errorf("decode current Goal criteria: %w", err)
	}
	if err := proposal.Validate(criteria, totalTasks, maxTasks); err != nil {
		return planner.PlanVersion{}, fmt.Errorf("revalidate Proposal against current Goal criteria: %w", err)
	}
	rows, err := tx.Query(ctx, "SELECT id::text,plan_key,status,title,description,success_criteria,criterion_ids FROM tasks WHERE goal_id=$1 FOR UPDATE", proposal.GoalID)
	if err != nil {
		return planner.PlanVersion{}, fmt.Errorf("lock existing Goal tasks: %w", err)
	}
	type existingTask struct {
		id           string
		key          string
		status       string
		title        string
		description  string
		criteria     []string
		criterionIDs []string
	}
	existing := make(map[string]existingTask)
	existingTasks := make([]existingTask, 0, totalTasks)
	for rows.Next() {
		var item existingTask
		if err := rows.Scan(&item.id, &item.key, &item.status, &item.title, &item.description, &item.criteria, &item.criterionIDs); err != nil {
			rows.Close()
			return planner.PlanVersion{}, err
		}
		existingTasks = append(existingTasks, item)
		if item.key != "" {
			existing[item.key] = item
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return planner.PlanVersion{}, err
	}
	rows.Close()
	if err := proposal.Plan.Validate(); err != nil {
		return planner.PlanVersion{}, err
	}
	keyToID := make(map[string]string, len(existing)+len(proposal.Plan.Tasks))
	for key, item := range existing {
		keyToID[key] = item.id
	}
	proposedKeys := make(map[string]bool, len(proposal.Plan.Tasks))
	for _, spec := range proposal.Plan.Tasks {
		proposedKeys[spec.Key] = true
	}
	for _, item := range existingTasks {
		if item.status != string(task.Completed) && item.status != string(task.Cancelled) && (item.key == "" || !proposedKeys[item.key]) {
			return planner.PlanVersion{}, fmt.Errorf("active Task %q must be retained in the new Plan; resolve or cancel it explicitly first", item.id)
		}
	}
	newSpecs := make([]planner.TaskSpec, 0)
	for _, spec := range proposal.Plan.Tasks {
		if old, ok := existing[spec.Key]; ok {
			if old.title != spec.Title || old.description != spec.Description {
				return planner.PlanVersion{}, fmt.Errorf("existing plan key %q cannot be modified in-place; create a new key", spec.Key)
			}
			if strings.Join(old.criterionIDs, "\x00") != strings.Join(spec.CriterionIDs, "\x00") {
				return planner.PlanVersion{}, fmt.Errorf("existing PlanKey %q cannot change criterion mapping; create a new key", spec.Key)
			}
			continue
		}
		newSpecs = append(newSpecs, spec)
	}
	if currentVersion > 0 && len(newSpecs) == 0 {
		if _, err := tx.Exec(ctx, "UPDATE plan_proposals SET status='rejected',reason='proposal contains no new executable task',resolved_at=now() WHERE id=$1", proposal.ID); err != nil {
			return planner.PlanVersion{}, fmt.Errorf("reject no-progress Plan Proposal: %w", err)
		}
		if err := recordMutation(ctx, tx, LocalOwnerID, "plan.rejected", "plan_proposals", proposal.ID, map[string]string{"reason": planner.ErrNoPlanProgress.Error()}); err != nil {
			return planner.PlanVersion{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return planner.PlanVersion{}, fmt.Errorf("commit no-progress rejection: %w", err)
		}
		return planner.PlanVersion{}, planner.ErrNoPlanProgress
	}
	if totalTasks+len(newSpecs) > maxTasks {
		return planner.PlanVersion{}, fmt.Errorf("plan commit would exceed total task budget")
	}
	created := make([]task.Task, len(newSpecs))
	for i, spec := range newSpecs {
		criterionIDs, err := json.Marshal(spec.CriterionIDs)
		if err != nil {
			return planner.PlanVersion{}, fmt.Errorf("encode proposed task criterion mappings: %w", err)
		}
		criteria, err := json.Marshal(spec.SuccessCriteria)
		if err != nil {
			return planner.PlanVersion{}, fmt.Errorf("encode proposed task success criteria: %w", err)
		}
		if spec.CriterionIDs == nil {
			criterionIDs = []byte("[]")
		}
		if spec.SuccessCriteria == nil {
			criteria = []byte("[]")
		}
		item := task.Task{GoalID: proposal.GoalID, PlanKey: spec.Key, Title: spec.Title, Description: spec.Description, Priority: 3, Status: task.Created, SuccessCriteria: spec.SuccessCriteria, CriterionIDs: spec.CriterionIDs}
		if err := item.Validate(); err != nil {
			return planner.PlanVersion{}, fmt.Errorf("validate proposed task %q: %w", spec.Key, err)
		}
		if err := tx.QueryRow(ctx, `INSERT INTO tasks(goal_id,title,description,priority,status,success_criteria,attempt_count,plan_key,criterion_ids)
			VALUES($1,$2,$3,$4,$5,$6,0,$7,$8) RETURNING id::text,created_at,updated_at`, item.GoalID, item.Title, item.Description, item.Priority, item.Status, criteria, item.PlanKey, criterionIDs).Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return planner.PlanVersion{}, fmt.Errorf("insert versioned plan task: %w", err)
		}
		created[i] = item
		keyToID[spec.Key] = item.ID
	}
	for i, spec := range newSpecs {
		deps := make([]string, 0, len(spec.DependsOn))
		for _, key := range spec.DependsOn {
			id, ok := keyToID[key]
			if !ok {
				return planner.PlanVersion{}, fmt.Errorf("task %q depends on unknown plan key %q", spec.Key, key)
			}
			deps = append(deps, id)
			if _, err := tx.Exec(ctx, "INSERT INTO task_dependencies(task_id,depends_on_task_id) VALUES($1,$2)", created[i].ID, id); err != nil {
				return planner.PlanVersion{}, fmt.Errorf("persist versioned plan dependency: %w", err)
			}
		}
		created[i].DependsOn = deps
		if err := recordMutation(ctx, tx, LocalOwnerID, "task.created", "tasks", created[i].ID, created[i]); err != nil {
			return planner.PlanVersion{}, err
		}
	}
	planPayload, err := json.Marshal(proposal.Plan)
	if err != nil {
		return planner.PlanVersion{}, err
	}
	if _, err := tx.Exec(ctx, "UPDATE plan_versions SET status='superseded' WHERE goal_id=$1 AND status='active'", proposal.GoalID); err != nil {
		return planner.PlanVersion{}, fmt.Errorf("supersede prior plan: %w", err)
	}
	var planID string
	var createdAt time.Time
	err = tx.QueryRow(ctx, `INSERT INTO plan_versions(goal_id,version,parent_version,status,planning_reason,trigger_type,evaluation_id,payload)
		VALUES($1,$2,$3,'active',$4,$5,NULLIF($6,'')::uuid,$7) RETURNING id::text,created_at`, proposal.GoalID, proposal.ProposedVersion, proposal.BasePlanVersion, proposal.Reason, proposal.Trigger, proposal.EvaluationID, planPayload).Scan(&planID, &createdAt)
	if err != nil {
		return planner.PlanVersion{}, fmt.Errorf("insert Plan Version: %w", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE plan_proposals SET status='accepted',resolved_at=now() WHERE id=$1", proposal.ID); err != nil {
		return planner.PlanVersion{}, fmt.Errorf("accept plan proposal: %w", err)
	}
	if err := recordMutation(ctx, tx, LocalOwnerID, "plan.accepted", "plan_versions", planID, map[string]any{"proposal_id": proposal.ID, "goal_id": proposal.GoalID, "version": proposal.ProposedVersion, "retained_tasks": proposal.RetainedTaskIDs, "added_tasks": created}); err != nil {
		return planner.PlanVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return planner.PlanVersion{}, fmt.Errorf("commit versioned Plan: %w", err)
	}
	return planner.PlanVersion{ID: planID, GoalID: proposal.GoalID, Version: proposal.ProposedVersion, ParentVersion: proposal.BasePlanVersion, Status: "active", PlanningReason: proposal.Reason, Trigger: proposal.Trigger, EvaluationID: proposal.EvaluationID, Plan: proposal.Plan, CreatedAt: createdAt}, nil
}

func (s *VersionedPlanStore) GetCurrentPlan(ctx context.Context, goalID string) (planner.PlanVersion, error) {
	return currentPlan(ctx, s.pool, goalID)
}

func (s *VersionedPlanStore) RejectProposal(ctx context.Context, goalID, proposalID, reason string) (planner.PlanProposal, error) {
	if strings.TrimSpace(reason) == "" || len(reason) > 4000 {
		return planner.PlanProposal{}, fmt.Errorf("rejection reason must contain 1 to 4000 characters")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return planner.PlanProposal{}, fmt.Errorf("begin Plan Proposal rejection: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var payload []byte
	var status string
	if err := tx.QueryRow(ctx, `SELECT status,payload FROM plan_proposals WHERE id=$1 AND goal_id=$2 FOR UPDATE`, proposalID, goalID).Scan(&status, &payload); errors.Is(err, pgx.ErrNoRows) {
		return planner.PlanProposal{}, planner.ErrProposalNotFound
	} else if err != nil {
		return planner.PlanProposal{}, fmt.Errorf("load Plan Proposal for rejection: %w", err)
	}
	if status != string(planner.ProposalProposed) {
		return planner.PlanProposal{}, fmt.Errorf("cannot reject Plan Proposal in status %q", status)
	}
	var item planner.PlanProposal
	if err := json.Unmarshal(payload, &item); err != nil {
		return planner.PlanProposal{}, fmt.Errorf("decode Plan Proposal for rejection: %w", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE plan_proposals SET status='rejected',reason=$2,resolved_at=now() WHERE id=$1 AND goal_id=$3", proposalID, reason, goalID); err != nil {
		return planner.PlanProposal{}, fmt.Errorf("persist Plan Proposal rejection: %w", err)
	}
	item.ID, item.Status, item.Reason = proposalID, planner.ProposalRejected, reason
	now := time.Now().UTC()
	item.ResolvedAt = &now
	if err := recordMutation(ctx, tx, LocalOwnerID, "plan.rejected", "plan_proposals", item.ID, item); err != nil {
		return planner.PlanProposal{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return planner.PlanProposal{}, fmt.Errorf("commit Plan Proposal rejection: %w", err)
	}
	return item, nil
}

func currentPlan(ctx context.Context, pool *pgxpool.Pool, goalID string) (planner.PlanVersion, error) {
	var item planner.PlanVersion
	var payload []byte
	err := pool.QueryRow(ctx, `SELECT id::text,goal_id::text,version,parent_version,status,planning_reason,trigger_type,COALESCE(evaluation_id::text,''),payload,created_at
		FROM plan_versions WHERE goal_id=$1 AND status='active'`, goalID).Scan(&item.ID, &item.GoalID, &item.Version, &item.ParentVersion, &item.Status, &item.PlanningReason, &item.Trigger, &item.EvaluationID, &payload, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return planner.PlanVersion{}, planner.ErrPlanConflict
	}
	if err != nil {
		return planner.PlanVersion{}, fmt.Errorf("get current Plan Version: %w", err)
	}
	if err := json.Unmarshal(payload, &item.Plan); err != nil {
		return planner.PlanVersion{}, fmt.Errorf("decode current Plan Version: %w", err)
	}
	return item, nil
}

func currentPlanInTx(ctx context.Context, tx pgx.Tx, goalID string) (planner.PlanVersion, error) {
	var item planner.PlanVersion
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT id::text,goal_id::text,version,parent_version,status,planning_reason,trigger_type,COALESCE(evaluation_id::text,''),payload,created_at
		FROM plan_versions WHERE goal_id=$1 AND status='active'`, goalID).Scan(&item.ID, &item.GoalID, &item.Version, &item.ParentVersion, &item.Status, &item.PlanningReason, &item.Trigger, &item.EvaluationID, &payload, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return planner.PlanVersion{}, planner.ErrPlanConflict
	}
	if err != nil {
		return planner.PlanVersion{}, fmt.Errorf("get current Plan Version in transaction: %w", err)
	}
	if err := json.Unmarshal(payload, &item.Plan); err != nil {
		return planner.PlanVersion{}, fmt.Errorf("decode current Plan Version: %w", err)
	}
	return item, nil
}

func (s *VersionedPlanStore) ListProposals(ctx context.Context, goalID string, limit int) ([]planner.PlanProposal, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text,status,payload,created_at,expires_at,resolved_at FROM plan_proposals
		WHERE goal_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2`, goalID, limit)
	if err != nil {
		return nil, fmt.Errorf("query plan proposals: %w", err)
	}
	defer rows.Close()
	items := make([]planner.PlanProposal, 0)
	for rows.Next() {
		var item planner.PlanProposal
		var status string
		var payload []byte
		if err := rows.Scan(&item.ID, &status, &payload, &item.CreatedAt, &item.ExpiresAt, &item.ResolvedAt); err != nil {
			return nil, fmt.Errorf("scan plan proposal: %w", err)
		}
		id := item.ID
		if err := json.Unmarshal(payload, &item); err != nil {
			return nil, fmt.Errorf("decode plan proposal: %w", err)
		}
		item.ID = id
		item.Status = planner.ProposalStatus(status)
		item.CreatedAt = item.CreatedAt.UTC()
		item.ExpiresAt = item.ExpiresAt.UTC()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plan proposals: %w", err)
	}
	return items, nil
}

var _ planner.ProposalRepository = (*VersionedPlanStore)(nil)
