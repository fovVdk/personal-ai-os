// English: This adapter applies row-locked Goal/Task transitions and crash-recovery queries.
// 中文：本适配器通过行锁更新 Goal/Task 状态，并提供崩溃恢复查询。
// 中文：本适配器通过行锁持久化 Goal/Task 状态转换，并查询中断后待恢复任务。
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/source"
	"personal-ai-os/internal/task"
	paierrors "personal-ai-os/pkg/errors"
)

func (p *Postgres) Update(ctx context.Context, ownerID string, item goal.Goal) (goal.Goal, error) {
	if err := goal.ValidateSuccessCriteria(item.Criteria); err != nil {
		return goal.Goal{}, fmt.Errorf("validate structured goal criteria: %w", err)
	}
	criteria, err := json.Marshal(item.SuccessCriteria)
	if err != nil {
		return goal.Goal{}, fmt.Errorf("encode goal criteria: %w", err)
	}
	constraints, err := json.Marshal(item.Constraints)
	if err != nil {
		return goal.Goal{}, fmt.Errorf("encode goal constraints: %w", err)
	}
	autonomy, err := json.Marshal(item.AutonomyPolicy)
	if err != nil {
		return goal.Goal{}, fmt.Errorf("encode goal autonomy policy: %w", err)
	}
	review, err := json.Marshal(item.ReviewPolicy)
	if err != nil {
		return goal.Goal{}, fmt.Errorf("encode goal review policy: %w", err)
	}
	if item.SuccessCriteria == nil {
		criteria = []byte("[]")
	}
	structuredCriteria, err := json.Marshal(item.Criteria)
	if err != nil {
		return goal.Goal{}, fmt.Errorf("encode structured goal criteria: %w", err)
	}
	if item.Criteria == nil {
		structuredCriteria = []byte("[]")
	}
	if item.Constraints == nil {
		constraints = []byte("[]")
	}
	if item.AutonomyPolicy == nil {
		autonomy = []byte("{}")
	}
	if item.ReviewPolicy == nil {
		review = []byte("{}")
	}
	var parent any
	if item.ParentGoalID != nil {
		parent = *item.ParentGoalID
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return goal.Goal{}, fmt.Errorf("begin goal update: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	err = tx.QueryRow(ctx, `UPDATE goals SET parent_goal_id=$3,title=$4,description=$5,priority=$6,success_criteria=$7,structured_success_criteria=$8,constraints=$9,
		autonomy_policy=$10,review_policy=$11,updated_at=now() WHERE owner_id=$1 AND id=$2
		RETURNING updated_at`, ownerID, item.ID, parent, item.Title, item.Description, item.Priority, criteria, structuredCriteria, constraints, autonomy, review).Scan(&item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return goal.Goal{}, paierrors.ErrNotFound
	}
	if err != nil {
		return goal.Goal{}, fmt.Errorf("update goal: %w", err)
	}
	item.OwnerID = ownerID
	if err := recordMutation(ctx, tx, ownerID, "goal.updated", "goals", item.ID, item); err != nil {
		return goal.Goal{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return goal.Goal{}, fmt.Errorf("commit goal update: %w", err)
	}
	return item, nil
}

func (p *Postgres) Transition(ctx context.Context, ownerID, id string, next goal.Status, evidence []goal.Evidence) (goal.Goal, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return goal.Goal{}, fmt.Errorf("begin goal transition: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var item goal.Goal
	var parent *string
	err = tx.QueryRow(ctx, `SELECT id::text,title,description,owner_id::text,parent_goal_id::text,priority,success_criteria,structured_success_criteria,constraints,
		autonomy_policy,review_policy,status,progress,created_at,updated_at,completed_at FROM goals WHERE owner_id=$1 AND id=$2 FOR UPDATE`, ownerID, id).Scan(
		&item.ID, &item.Title, &item.Description, &item.OwnerID, &parent, &item.Priority, &item.SuccessCriteria, &item.Criteria, &item.Constraints,
		&item.AutonomyPolicy, &item.ReviewPolicy, &item.Status, &item.Progress, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return goal.Goal{}, paierrors.ErrNotFound
	}
	if err != nil {
		return goal.Goal{}, fmt.Errorf("load goal transition: %w", err)
	}
	item.ParentGoalID = parent
	if next == goal.Completed {
		verifiedEvidence, err := p.requireFreshCriteriaEvaluation(ctx, tx, item)
		if err != nil {
			return goal.Goal{}, err
		}
		// Completion evidence is derived from the persisted Evaluation, never from
		// caller-supplied prose. The request's evidence field cannot satisfy criteria.
		// 中文：完成证据必须从持久化 Evaluation 推导，不能使用调用方自然语言；请求中的 evidence 字段不能满足成功标准。
		evidence = verifiedEvidence
	}
	if next == goal.Completed {
		var taskStatus string
		err := tx.QueryRow(ctx, "SELECT status FROM tasks WHERE goal_id=$1 AND status NOT IN ('completed','cancelled') LIMIT 1", item.ID).Scan(&taskStatus)
		if err == nil {
			return goal.Goal{}, goal.ErrCriteriaUnverified
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return goal.Goal{}, fmt.Errorf("recheck unsettled Tasks before Goal completion: %w", err)
		}
	}
	now := time.Now().UTC()
	if err := item.Transition(next, evidence, now); err != nil {
		return goal.Goal{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE goals SET status=$3,progress=$4,updated_at=$5,completed_at=$6 WHERE owner_id=$1 AND id=$2`, ownerID, id, item.Status, item.Progress, item.UpdatedAt, item.CompletedAt); err != nil {
		return goal.Goal{}, fmt.Errorf("persist goal transition: %w", err)
	}
	if next == goal.Completed {
		for _, evidenceItem := range item.Evidence {
			_, err := tx.Exec(ctx, `INSERT INTO goal_evidence(goal_id,source,description,reference,observed_at) VALUES($1,$2,$3,$4,$5)`, item.ID, evidenceItem.Source, evidenceItem.Description, evidenceItem.Reference, evidenceItem.ObservedAt.UTC())
			if err != nil {
				return goal.Goal{}, fmt.Errorf("persist goal evidence: %w", err)
			}
		}
	}
	eventType := "goal.updated"
	if next == goal.Completed {
		eventType = "goal.completed"
	}
	if err := recordMutation(ctx, tx, ownerID, eventType, "goals", item.ID, item); err != nil {
		return goal.Goal{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return goal.Goal{}, fmt.Errorf("commit goal transition: %w", err)
	}
	return item, nil
}

func (p *Postgres) requireFreshCriteriaEvaluation(ctx context.Context, tx pgx.Tx, item goal.Goal) ([]goal.Evidence, error) {
	if len(item.Criteria) == 0 || len(item.SuccessCriteria) > 0 {
		return nil, goal.ErrCriteriaUnverified
	}
	required := make(map[string]struct{}, len(item.Criteria))
	for _, criterion := range item.Criteria {
		if !criterion.Required {
			continue
		}
		if !criterion.Confirmed {
			return nil, goal.ErrCriteriaUnverified
		}
		required[criterion.ID] = struct{}{}
	}
	if len(required) == 0 {
		return nil, goal.ErrCriteriaUnverified
	}
	var evaluationID string
	var evaluatedAt, goalUpdatedAt time.Time
	var evaluationStatus, recommendedAction string
	err := tx.QueryRow(ctx, `SELECT id::text,evaluated_at,goal_updated_at,evaluation_status,recommended_action FROM goal_evaluations
		WHERE goal_id=$1 AND goal_updated_at=$2 ORDER BY evaluated_at DESC,id DESC LIMIT 1`, item.ID, item.UpdatedAt).Scan(&evaluationID, &evaluatedAt, &goalUpdatedAt, &evaluationStatus, &recommendedAction)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, goal.ErrCriteriaUnverified
	}
	if err != nil {
		return nil, fmt.Errorf("load current success-criteria evaluation: %w", err)
	}
	if evaluationStatus != "ready_for_verification" || recommendedAction != "verify_completion" {
		return nil, goal.ErrCriteriaUnverified
	}
	verifiedEvidence := make([]goal.Evidence, 0, len(required))
	criterionNames := make(map[string]goal.SuccessCriterion, len(item.Criteria))
	for _, criterion := range item.Criteria {
		criterionNames[criterion.ID] = criterion
	}
	rows, err := tx.Query(ctx, `SELECT cv.id::text,cv.criterion_id,cv.criterion_version,cv.status,cv.reason,count(ve.evidence_id)
		FROM criterion_verifications cv LEFT JOIN criterion_verification_evidence ve ON ve.verification_id=cv.id
		WHERE cv.evaluation_id=$1 AND cv.goal_id=$2 GROUP BY cv.id ORDER BY cv.criterion_id`, evaluationID, item.ID)
	if err != nil {
		return nil, fmt.Errorf("load persisted criterion verifications: %w", err)
	}
	type verificationRecord struct {
		id            string
		criterionID   string
		version       int
		status        string
		reason        string
		evidenceCount int
	}
	records := make([]verificationRecord, 0)
	for rows.Next() {
		var record verificationRecord
		if err := rows.Scan(&record.id, &record.criterionID, &record.version, &record.status, &record.reason, &record.evidenceCount); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan persisted criterion verification: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate persisted criterion verifications: %w", err)
	}
	rows.Close()
	for _, record := range records {
		criterion, exists := criterionNames[record.criterionID]
		if !exists || !criterion.Required || criterion.Version != record.version || record.status != "satisfied" || record.evidenceCount < 1 {
			continue
		}
		if err := p.hasPersistedVerificationHash(ctx, tx, record.id, item.OwnerID, item.ID, criterion); err != nil {
			return nil, fmt.Errorf("%w: criterion %s: %v", goal.ErrCriteriaUnverified, record.criterionID, err)
		}
		if _, ok := required[record.criterionID]; !ok {
			continue
		}
		delete(required, record.criterionID)
		verifiedEvidence = append(verifiedEvidence, goal.Evidence{
			Source:      "structured-criterion-verifier",
			Description: fmt.Sprintf("required criterion %q (%s) satisfied: %s; %d persisted evidence item(s)", criterion.Name, criterion.Type, record.reason, record.evidenceCount),
			Reference:   "goal-evaluation:" + evaluationID + "#" + record.criterionID,
			ObservedAt:  evaluatedAt.UTC(),
		})
	}
	if len(required) > 0 {
		return nil, goal.ErrCriteriaUnverified
	}
	if len(verifiedEvidence) == 0 {
		return nil, goal.ErrCriteriaUnverified
	}
	var latestTaskUpdate, latestExecutionCompletion time.Time
	var unsettledTasks int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM tasks WHERE goal_id=$1 AND status NOT IN ('completed','cancelled')", item.ID).Scan(&unsettledTasks); err != nil {
		return nil, fmt.Errorf("check unsettled tasks before Goal completion: %w", err)
	}
	if unsettledTasks != 0 {
		return nil, goal.ErrCriteriaUnverified
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(updated_at),$2) FROM tasks WHERE goal_id=$1`, item.ID, item.UpdatedAt).Scan(&latestTaskUpdate); err != nil {
		return nil, fmt.Errorf("check task freshness for success criteria: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(e.completed_at),$2) FROM execution_runs e JOIN tasks t ON t.id=e.task_id
		WHERE t.goal_id=$1 AND e.completed_at IS NOT NULL`, item.ID, item.UpdatedAt).Scan(&latestExecutionCompletion); err != nil {
		return nil, fmt.Errorf("check execution freshness for success criteria: %w", err)
	}
	if goalUpdatedAt.Before(item.UpdatedAt) || evaluatedAt.Before(latestTaskUpdate) || evaluatedAt.Before(latestExecutionCompletion) {
		return nil, goal.ErrCriteriaUnverified
	}
	return verifiedEvidence, nil
}

func (p *Postgres) hasPersistedVerificationHash(ctx context.Context, tx pgx.Tx, verificationID, ownerID, goalID string, criterion goal.SuccessCriterion) error {
	if criterion.Type != goal.CriterionArtifactExists && criterion.Type != goal.CriterionMarkdownStructure {
		return nil
	}
	var contentHash string
	var reference string
	err := tx.QueryRow(ctx, `SELECT ev.content_hash,ev.reference FROM criterion_verification_evidence link
		JOIN execution_evidence ev ON ev.id=link.evidence_id
		WHERE link.verification_id=$1 AND ev.criterion_id=$2 AND ev.criterion_version=$3
		AND ev.verifier_type='core.file-artifact' AND ev.verifier_version='v1'
		AND length(ev.content_hash)=64 AND ev.reference LIKE $4 LIMIT 1`, verificationID, criterion.ID, criterion.Version, "artifact://"+strings.TrimPrefix(criterion.ArtifactPath, "./")+"#sha256=%").Scan(&contentHash, &reference)
	if err != nil {
		return fmt.Errorf("verified artifact Evidence record is missing: %w", err)
	}
	if !strings.HasSuffix(reference, "#sha256="+contentHash) {
		return fmt.Errorf("artifact reference does not bind the recorded SHA-256")
	}
	if p.artifactVerifier == nil {
		return fmt.Errorf("Core artifact freshness verifier is not configured")
	}
	if err := p.artifactVerifier.VerifyArtifactHash(ctx, criterion.ArtifactPath, contentHash); err != nil {
		return fmt.Errorf("current artifact no longer matches verified hash: %w", err)
	}
	if criterion.RequireReference {
		rows, err := tx.Query(ctx, `SELECT sr.snapshot_id::text,sr.citation_url,sr.quote,sr.quote_hash,
			ss.original_url,ss.final_url,ss.http_status,ss.mime_type,ss.retrieved_at,ss.content_hash,ss.raw_content,ss.normalized_content,
			EXISTS(SELECT 1 FROM criterion_verification_evidence link JOIN execution_evidence ev ON ev.id=link.evidence_id
				WHERE link.verification_id=$1 AND ev.criterion_id=$2 AND ev.criterion_version=$3
				AND ev.verifier_type='core.source-reference' AND ev.verifier_version='v1'
				AND ev.reference='source://'||ss.id::text||'#sha256='||ss.content_hash AND ev.content_hash=ss.content_hash)
			FROM source_references sr JOIN source_snapshots ss ON ss.id=sr.snapshot_id
			WHERE sr.owner_id=$4 AND sr.goal_id=$5 AND sr.artifact_path=$6
			ORDER BY sr.id FOR SHARE OF sr,ss`, verificationID, criterion.ID, criterion.Version, ownerID, goalID, criterion.ArtifactPath)
		if err != nil {
			return fmt.Errorf("lock and reload source references for completion: %w", err)
		}
		defer rows.Close()
		count := 0
		now := time.Now().UTC()
		for rows.Next() {
			var snapshotID, citationURL, quote, quoteHash, originalURL, finalURL string
			var status int
			var mimeType string
			var retrievedAt time.Time
			var snapshotHash string
			var rawContent []byte
			var normalizedContent string
			var evidenceBound bool
			if err := rows.Scan(&snapshotID, &citationURL, &quote, &quoteHash, &originalURL, &finalURL, &status, &mimeType, &retrievedAt, &snapshotHash, &rawContent, &normalizedContent, &evidenceBound); err != nil {
				return fmt.Errorf("scan source reference at Goal completion: %w", err)
			}
			count++
			if !evidenceBound {
				return fmt.Errorf("current verification does not bind source snapshot %s", snapshotID)
			}
			if citationURL != originalURL && citationURL != finalURL {
				return errors.New("report source reference URL no longer matches its snapshot")
			}
			if status < 200 || status >= 300 || retrievedAt.After(now) || now.Sub(retrievedAt) > source.DefaultSnapshotMaxAge {
				return errors.New("report source snapshot is unsuccessful, future-dated, or stale")
			}
			if source.Hash(rawContent) != snapshotHash || normalizedContent != source.NormalizeContent(mimeType, rawContent) {
				return errors.New("report source snapshot content integrity check failed")
			}
			normalizedQuote := strings.Join(strings.Fields(quote), " ")
			if source.Hash([]byte(normalizedQuote)) != quoteHash || !strings.Contains(normalizedContent, normalizedQuote) {
				return errors.New("report source quote no longer matches the immutable snapshot")
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate source references at Goal completion: %w", err)
		}
		if count == 0 {
			return errors.New("required report source references are missing")
		}
	}
	return nil
}

func (p *Postgres) TransitionTask(ctx context.Context, id string, next task.Status) (task.Task, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return task.Task{}, fmt.Errorf("begin task transition: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var goalID string
	if err := tx.QueryRow(ctx, "SELECT goal_id::text FROM tasks WHERE id=$1", id).Scan(&goalID); errors.Is(err, pgx.ErrNoRows) {
		return task.Task{}, paierrors.ErrNotFound
	} else if err != nil {
		return task.Task{}, fmt.Errorf("load Task Goal before transition: %w", err)
	}
	if err := lockGoalShared(ctx, tx, goalID); err != nil {
		return task.Task{}, fmt.Errorf("lock Goal before Task transition: %w", err)
	}
	var item task.Task
	err = tx.QueryRow(ctx, `SELECT id::text,goal_id::text,title,description,priority,status,attempt_count,success_criteria,plan_key,criterion_ids,scheduled_at,started_at,completed_at,created_at,updated_at
		FROM tasks WHERE id=$1 FOR UPDATE`, id).Scan(&item.ID, &item.GoalID, &item.Title, &item.Description, &item.Priority, &item.Status, &item.AttemptCount, &item.SuccessCriteria,
		&item.PlanKey, &item.CriterionIDs,
		&item.ScheduledAt, &item.StartedAt, &item.CompletedAt, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return task.Task{}, paierrors.ErrNotFound
	}
	if err != nil {
		return task.Task{}, fmt.Errorf("load task transition: %w", err)
	}
	if err := item.Transition(next, time.Now().UTC()); err != nil {
		return task.Task{}, err
	}
	if next == task.Running {
		item.AttemptCount++
	}
	if _, err := tx.Exec(ctx, `UPDATE tasks SET status=$2,started_at=$3,completed_at=$4,updated_at=$5,attempt_count=$6 WHERE id=$1`, item.ID, item.Status, item.StartedAt, item.CompletedAt, item.UpdatedAt, item.AttemptCount); err != nil {
		return task.Task{}, fmt.Errorf("persist task transition: %w", err)
	}
	eventType := "task.updated"
	switch next {
	case task.Running:
		eventType = "task.started"
	case task.Completed:
		eventType = "task.completed"
	case task.Failed:
		eventType = "task.failed"
	}
	if err := recordMutation(ctx, tx, LocalOwnerID, eventType, "tasks", item.ID, item); err != nil {
		return task.Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return task.Task{}, fmt.Errorf("commit task transition: %w", err)
	}
	return item, nil
}

var _ goal.Repository = (*Postgres)(nil)

func (p *Postgres) loadGoalEvidence(ctx context.Context, goalID string) ([]goal.Evidence, error) {
	rows, err := p.pool.Query(ctx, `SELECT source,description,COALESCE(reference,''),observed_at FROM goal_evidence WHERE goal_id=$1 ORDER BY observed_at`, goalID)
	if err != nil {
		return nil, fmt.Errorf("load goal evidence: %w", err)
	}
	defer rows.Close()
	items := make([]goal.Evidence, 0)
	for rows.Next() {
		var item goal.Evidence
		if err := rows.Scan(&item.Source, &item.Description, &item.Reference, &item.ObservedAt); err != nil {
			return nil, fmt.Errorf("scan goal evidence: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate goal evidence: %w", err)
	}
	return items, nil
}

func (p *Postgres) Recoverable(ctx context.Context, limit int) ([]task.Task, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := p.pool.Query(ctx, `SELECT id::text,goal_id::text,title,description,priority,status,attempt_count,success_criteria,plan_key,criterion_ids,scheduled_at,started_at,completed_at,created_at,updated_at
		FROM tasks WHERE status IN ('running','retrying') ORDER BY updated_at LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("load recoverable tasks: %w", err)
	}
	defer rows.Close()
	items := make([]task.Task, 0)
	for rows.Next() {
		var item task.Task
		if err := rows.Scan(&item.ID, &item.GoalID, &item.Title, &item.Description, &item.Priority, &item.Status, &item.AttemptCount, &item.SuccessCriteria, &item.PlanKey, &item.CriterionIDs, &item.ScheduledAt, &item.StartedAt, &item.CompletedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan recoverable task: %w", err)
		}
		item.DependsOn, err = p.taskDependencies(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recoverable tasks: %w", err)
	}
	return items, nil
}

var _ interface {
	Recoverable(context.Context, int) ([]task.Task, error)
} = (*Postgres)(nil)
