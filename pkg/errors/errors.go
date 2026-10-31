// English: This file declares stable cross-package domain errors for API translation.
// 中文：本文件声明跨包共享的稳定领域错误，供 API 转换为一致响应。
package errors

import "errors"

var (
	ErrNotFound     = errors.New("resource not found")
	ErrConflict     = errors.New("resource conflict")
	ErrUnauthorized = errors.New("unauthorized")
)
