// English: Repository isolates Knowledge document persistence from HTTP and indexing callers.
// 中文：Repository 将知识文档持久化与 HTTP/索引调用方隔离。
package knowledge

import "context"

// Repository persists content-addressed documents and supports cited retrieval.
// 中文：Repository 持久化内容寻址文档，并提供可返回引用的检索能力。
type Repository interface {
	Import(context.Context, Document) (Document, error)
	List(context.Context, string, int) ([]Document, error)
	Search(context.Context, string, string, int) ([]Citation, error)
}
