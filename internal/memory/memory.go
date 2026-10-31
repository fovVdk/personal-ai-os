// Package memory manages provenance-aware personal memories.
// 中文：memory 包管理保留来源信息的个人记忆。
// English: This file defines provenance-aware memory records and their owner-scoped repository.
// 中文：本文件定义保留来源信息的个人记忆记录及按所有者隔离的仓储接口。
package memory

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Category string

const (
	Fact         Category = "fact"
	Preference   Category = "preference"
	Experience   Category = "experience"
	Relationship Category = "relationship"
	Event        Category = "event"
	Instruction  Category = "instruction"
)

type Memory struct {
	ID         string         `json:"id"`
	OwnerID    string         `json:"owner_id"`
	Category   Category       `json:"category"`
	Content    string         `json:"content"`
	Source     string         `json:"source"`
	Confidence float64        `json:"confidence"`
	ObservedAt *time.Time     `json:"observed_at,omitempty"`
	Metadata   map[string]any `json:"metadata"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

func (m Memory) Validate() error {
	if strings.TrimSpace(m.Content) == "" {
		return errors.New("memory content is required")
	}
	if strings.TrimSpace(m.Source) == "" {
		return errors.New("memory source is required")
	}
	if m.Confidence < 0 || m.Confidence > 1 {
		return errors.New("memory confidence must be between 0 and 1")
	}
	switch m.Category {
	case Fact, Preference, Experience, Relationship, Event, Instruction:
		return nil
	default:
		return errors.New("unsupported memory category")
	}
}

// Repository is the persistence boundary for owner-scoped memory records.
// 中文：Repository 定义按所有者隔离的记忆记录持久化边界。
type Repository interface {
	Create(context.Context, Memory) (Memory, error)
	Get(context.Context, string, string) (Memory, error)
	List(context.Context, string, int) ([]Memory, error)
	Update(context.Context, string, Memory) (Memory, error)
	Delete(context.Context, string, string) error
	Search(context.Context, string, string, int) ([]Memory, error)
}
