// English: Shared Goal row locks serialize Task/Evidence mutations against Goal completion.
// 中文：Goal 行共享锁将 Task/Evidence 修改与 Goal 完成门禁串行化。
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func lockGoalShared(ctx context.Context, tx pgx.Tx, goalID string) error {
	var id string
	err := tx.QueryRow(ctx, "SELECT id::text FROM goals WHERE id=$1 FOR SHARE", goalID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock Goal for shared mutation: %w", err)
	}
	return nil
}
