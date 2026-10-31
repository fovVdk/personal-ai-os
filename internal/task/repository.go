// English: Repository defines the durable task persistence and transition boundary.
// 中文：Repository 定义任务持久化及生命周期转换的存储边界。
package task

import "context"

// Repository persists tasks independently of API and agent runtimes.
// 中文：Repository 独立于 API 和 Agent Runtime 持久化任务。
type Repository interface {
	Create(context.Context, Task) (Task, error)
	CreateMany(context.Context, []Task) ([]Task, error)
	Get(context.Context, string) (Task, error)
	ListByGoal(context.Context, string, int) ([]Task, error)
	Transition(context.Context, string, Status) (Task, error)
}
