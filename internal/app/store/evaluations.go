// English: This adapter persists immutable Goal Evaluation snapshots with audit/outbox records.
// 中文：本适配器不可变地保存 Goal Evaluation 快照，并同步写入审计和事件 Outbox。
package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"personal-ai-os/internal/supervisor"
)

type EvaluationStore struct{ pool *pgxpool.Pool }

func NewEvaluationStore(pool *pgxpool.Pool) *EvaluationStore {
	return &EvaluationStore{pool: pool}
}

func (s *EvaluationStore) SaveEvaluation(ctx context.Context, item supervisor.Evaluation) (supervisor.Evaluation, error) {
	if item.GoalID == "" || item.GoalUpdatedAt.IsZero() || item.RecommendedAction == "" || item.Status == "" {
		return supervisor.Evaluation{}, fmt.Errorf("evaluation snapshot is incomplete")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return supervisor.Evaluation{}, fmt.Errorf("begin goal evaluation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := tx.QueryRow(ctx, `INSERT INTO goal_evaluations(goal_id,goal_updated_at,plan_version,evaluation_status,recommended_action,payload,evaluated_at)
		VALUES($1,$2,$3,$4,$5,'{}'::jsonb,$6) RETURNING id::text`, item.GoalID, item.GoalUpdatedAt.UTC(), item.PlanVersion, item.Status, item.RecommendedAction, item.CreatedAt.UTC()).Scan(&item.ID); err != nil {
		return supervisor.Evaluation{}, fmt.Errorf("insert goal evaluation: %w", err)
	}
	for i := range item.CompletedCriteria {
		if err := persistCriterionVerification(ctx, tx, item, &item.CompletedCriteria[i]); err != nil {
			return supervisor.Evaluation{}, err
		}
	}
	for i := range item.UnmetCriteria {
		if err := persistCriterionVerification(ctx, tx, item, &item.UnmetCriteria[i]); err != nil {
			return supervisor.Evaluation{}, err
		}
	}
	decision, err := supervisor.NewDecision(item)
	if err != nil {
		return supervisor.Evaluation{}, fmt.Errorf("derive Supervisor decision: %w", err)
	}
	decision, created, err := saveDecisionTx(ctx, tx, decision)
	if err != nil {
		return supervisor.Evaluation{}, err
	}
	item.DecisionID = decision.ID
	if created {
		if err := recordMutation(ctx, tx, LocalOwnerID, "supervisor.decision", "supervisor_decisions", decision.ID, decision); err != nil {
			return supervisor.Evaluation{}, err
		}
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return supervisor.Evaluation{}, fmt.Errorf("encode goal evaluation: %w", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE goal_evaluations SET payload=$2 WHERE id=$1", item.ID, payload); err != nil {
		return supervisor.Evaluation{}, fmt.Errorf("persist goal evaluation payload: %w", err)
	}
	if err := recordMutation(ctx, tx, LocalOwnerID, "goal.evaluated", "goal_evaluations", item.ID, item); err != nil {
		return supervisor.Evaluation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return supervisor.Evaluation{}, fmt.Errorf("commit goal evaluation: %w", err)
	}
	return item, nil
}

func persistCriterionVerification(ctx context.Context, tx pgx.Tx, evaluation supervisor.Evaluation, assessment *supervisor.CriterionAssessment) error {
	if assessment.CriterionID == "" || assessment.CriterionVersion < 1 || assessment.VerifierType == "" || assessment.VerifierVersion == "" {
		return nil
	}
	evidenceHashes, err := json.Marshal(assessment.EvidenceHashes)
	if err != nil {
		return fmt.Errorf("encode criterion verification hashes: %w", err)
	}
	if assessment.EvidenceHashes == nil {
		evidenceHashes = []byte("[]")
	}
	err = tx.QueryRow(ctx, `INSERT INTO criterion_verifications(evaluation_id,goal_id,goal_updated_at,criterion_id,criterion_version,verifier_type,verifier_version,status,reason,evidence_hashes)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id::text`, evaluation.ID, evaluation.GoalID, evaluation.GoalUpdatedAt.UTC(), assessment.CriterionID, assessment.CriterionVersion, assessment.VerifierType, assessment.VerifierVersion, assessment.Status, assessment.Reason, evidenceHashes).Scan(&assessment.VerificationID)
	if err != nil {
		return fmt.Errorf("persist criterion verification: %w", err)
	}
	if len(assessment.Evidence) > 0 {
		tag, err := tx.Exec(ctx, `INSERT INTO criterion_verification_evidence(verification_id,evidence_id)
			SELECT $1,ev.id FROM execution_evidence ev JOIN execution_runs er ON er.id=ev.execution_run_id
			JOIN tasks t ON t.id=er.task_id WHERE ev.id::text=ANY($2::text[]) AND t.goal_id=$3
			ON CONFLICT DO NOTHING`, assessment.VerificationID, assessment.Evidence, evaluation.GoalID)
		if err != nil {
			return fmt.Errorf("link criterion verification evidence: %w", err)
		}
		if tag.RowsAffected() != int64(len(assessment.Evidence)) {
			return fmt.Errorf("criterion verification references %d evidence item(s), but only %d exist for this Goal", len(assessment.Evidence), tag.RowsAffected())
		}
	}
	return nil
}

func (s *EvaluationStore) ListEvaluations(ctx context.Context, goalID string, limit int) ([]supervisor.Evaluation, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `SELECT payload FROM goal_evaluations WHERE goal_id=$1 ORDER BY evaluated_at DESC,id DESC LIMIT $2`, goalID, limit)
	if err != nil {
		return nil, fmt.Errorf("query goal evaluations: %w", err)
	}
	defer rows.Close()
	items := make([]supervisor.Evaluation, 0)
	for rows.Next() {
		var payload []byte
		var item supervisor.Evaluation
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan goal evaluation: %w", err)
		}
		if err := json.Unmarshal(payload, &item); err != nil {
			return nil, fmt.Errorf("decode goal evaluation: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate goal evaluations: %w", err)
	}
	return items, nil
}

var _ supervisor.EvaluationStore = (*EvaluationStore)(nil)
