// Package planner defines validated, structured plans independent of model vendors.
// 中文：planner 包定义经过校验且与模型供应商无关的结构化计划。
// English: This file defines structured plan contracts and validates the task dependency DAG.
// 中文：本文件定义结构化计划契约，并验证任务依赖关系是否构成 DAG。
package planner

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
)

type TaskSpec struct {
	Key             string   `json:"key"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	SuccessCriteria []string `json:"success_criteria"`
	DependsOn       []string `json:"depends_on"`
	EstimatedHours  float64  `json:"estimated_hours"`
	Risks           []string `json:"risks"`
}

type Plan struct {
	GoalID            string     `json:"goal_id"`
	Summary           string     `json:"summary"`
	Tasks             []TaskSpec `json:"tasks"`
	EstimatedHours    float64    `json:"estimated_hours"`
	Risks             []string   `json:"risks"`
	ModelCalls        int        `json:"-"`
	ModelTokens       int        `json:"-"`
	ModelInputTokens  int        `json:"-"`
	ModelOutputTokens int        `json:"-"`
}

type Generator interface {
	GenerateContext(context.Context, GoalInput) (Plan, error)
}

type GoalInput struct {
	ID              string
	Title           string
	Description     string
	SuccessCriteria []string
	Constraints     []string
}

func (p Plan) Validate() error {
	if p.GoalID == "" {
		return errors.New("plan goal_id is required")
	}
	if strings.TrimSpace(p.Summary) == "" {
		return errors.New("plan summary is required")
	}
	if len(p.Tasks) == 0 {
		return errors.New("plan must contain at least one task")
	}
	if p.EstimatedHours < 0 || math.IsNaN(p.EstimatedHours) || math.IsInf(p.EstimatedHours, 0) {
		return errors.New("plan estimate must be finite and non-negative")
	}
	keys := make(map[string]TaskSpec, len(p.Tasks))
	for _, task := range p.Tasks {
		if strings.TrimSpace(task.Key) == "" || strings.TrimSpace(task.Title) == "" {
			return errors.New("each planned task requires key and title")
		}
		if _, exists := keys[task.Key]; exists {
			return fmt.Errorf("duplicate task key %q", task.Key)
		}
		if task.EstimatedHours < 0 || math.IsNaN(task.EstimatedHours) || math.IsInf(task.EstimatedHours, 0) {
			return fmt.Errorf("task %q estimate must be finite and non-negative", task.Key)
		}
		keys[task.Key] = task
	}
	indegree := make(map[string]int, len(keys))
	dependents := make(map[string][]string, len(keys))
	for _, task := range p.Tasks {
		seen := make(map[string]struct{}, len(task.DependsOn))
		for _, dependency := range task.DependsOn {
			if _, ok := keys[dependency]; !ok {
				return fmt.Errorf("task %q depends on unknown task %q", task.Key, dependency)
			}
			if dependency == task.Key {
				return fmt.Errorf("task %q cannot depend on itself", task.Key)
			}
			if _, ok := seen[dependency]; ok {
				return fmt.Errorf("task %q repeats dependency %q", task.Key, dependency)
			}
			seen[dependency] = struct{}{}
			indegree[task.Key]++
			dependents[dependency] = append(dependents[dependency], task.Key)
		}
	}
	queue := make([]string, 0, len(keys))
	for key := range keys {
		if indegree[key] == 0 {
			queue = append(queue, key)
		}
	}
	visited := 0
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		visited++
		for _, dependent := range dependents[key] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}
	if visited != len(keys) {
		return errors.New("plan dependencies contain a cycle")
	}
	return nil
}
