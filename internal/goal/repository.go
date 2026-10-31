// English: Repository defines the persistence boundary for owner-scoped Goal aggregates.
// 中文：Repository 定义按所有者隔离的 Goal 聚合持久化边界。
package goal

import "context"

// Repository persists goals independently of API and agent runtimes.
// 中文：Repository 独立于 API 和 Agent Runtime 持久化 Goal。
type Repository interface {
	Create(context.Context, Goal) (Goal, error)
	Get(context.Context, string, string) (Goal, error)
	List(context.Context, string, int) ([]Goal, error)
	Update(context.Context, string, Goal) (Goal, error)
	Transition(context.Context, string, string, Status, []Evidence) (Goal, error)
}
