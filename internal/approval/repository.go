// English: This interface isolates approval workflows from their database adapter.
// 中文：本接口将审批流程与具体数据库存储实现解耦。
package approval

import (
	"context"
	"time"
)

// Repository persists approval requests and serialized human decisions.
// 中文：Repository 持久化审批请求，并串行化人工决策状态。
type Repository interface {
	Create(context.Context, Request) (Request, error)
	ListPending(context.Context, string, int) ([]Request, error)
	Get(context.Context, string, string) (Request, error)
	Decide(context.Context, string, string, Status, time.Time) (Request, error)
}
