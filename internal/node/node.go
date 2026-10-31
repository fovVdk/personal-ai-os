// Package node defines identities and health state for future local/distributed workers.
// 中文：node 包定义本地及未来分布式 Worker 的身份和健康状态。
// English: This file defines node identity, capabilities, health state, and repository contracts.
// 中文：本文件定义节点身份、能力、健康状态及节点仓储契约。
package node

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Type string

const (
	Core   Type = "core"
	Worker Type = "worker"
	Vision Type = "vision"
	Robot  Type = "robot"
	Home   Type = "home"
)

type Status string

const (
	Online   Status = "online"
	Degraded Status = "degraded"
	Offline  Status = "offline"
)

type Node struct {
	ID            string     `json:"id"`
	Identity      string     `json:"identity"`
	Name          string     `json:"name"`
	Type          Type       `json:"type"`
	Capabilities  []string   `json:"capabilities"`
	Status        Status     `json:"status"`
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (n Node) Validate() error {
	if strings.TrimSpace(n.Identity) == "" || strings.TrimSpace(n.Name) == "" {
		return errors.New("node identity and name are required")
	}
	switch n.Type {
	case Core, Worker, Vision, Robot, Home:
	default:
		return errors.New("unsupported node type")
	}
	if n.Status != "" && n.Status != Online && n.Status != Degraded && n.Status != Offline {
		return errors.New("unsupported node status")
	}
	for _, capability := range n.Capabilities {
		if strings.TrimSpace(capability) == "" {
			return errors.New("node capability must not be empty")
		}
	}
	return nil
}

type Repository interface {
	Register(context.Context, Node) (Node, error)
	List(context.Context, int) ([]Node, error)
	Get(context.Context, string) (Node, error)
	Heartbeat(context.Context, string, Status, time.Time) (Node, error)
}
