// Package registry stores versioned agent capabilities behind the runtime interface.
// 中文：registry 包通过 Runtime 接口管理带版本的 Agent 能力。
// English: This file stores versioned agent profiles behind framework-neutral runtime interfaces.
// 中文：本文件通过与框架无关的运行时接口保存带版本的 Agent 配置。
package registry

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"personal-ai-os/internal/agent/runtime"
)

var ErrNotFound = errors.New("agent not found")

type Profile struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Version      string        `json:"version"`
	Capabilities []string      `json:"capabilities"`
	Agent        runtime.Agent `json:"-"`
}

type Registry struct {
	mu     sync.RWMutex
	agents map[string]Profile
}

func New() *Registry { return &Registry{agents: make(map[string]Profile)} }
func (r *Registry) Register(profile Profile) error {
	if profile.ID == "" || profile.Name == "" || profile.Version == "" || profile.Agent == nil {
		return errors.New("agent id, name, version and implementation are required")
	}
	if profile.Agent.ID() != profile.ID {
		return errors.New("agent implementation id must match profile id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.agents[profile.ID]; exists {
		return fmt.Errorf("agent %q is already registered", profile.ID)
	}
	profile.Capabilities = append([]string(nil), profile.Capabilities...)
	r.agents[profile.ID] = profile
	return nil
}
func (r *Registry) Get(id string) (Profile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, ok := r.agents[id]
	if !ok {
		return Profile{}, ErrNotFound
	}
	profile.Capabilities = append([]string(nil), profile.Capabilities...)
	return profile, nil
}
func (r *Registry) List() []Profile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]Profile, 0, len(r.agents))
	for _, profile := range r.agents {
		profile.Capabilities = append([]string(nil), profile.Capabilities...)
		items = append(items, profile)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}
