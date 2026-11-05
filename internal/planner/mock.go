// English: MockGenerator creates a deterministic, reviewable plan without a model service.
// 中文：MockGenerator 无需模型服务即可生成确定性、可供审查的计划。
package planner

import (
	"context"
	"fmt"

	goaldomain "personal-ai-os/internal/goal"
)

// MockGenerator produces a conservative, deterministic plan without external models.
// 中文：MockGenerator 无需外部模型即可生成保守且确定性的计划。
type MockGenerator struct{}

func (MockGenerator) GenerateContext(ctx context.Context, goal GoalInput) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if goal.ID == "" {
		return Plan{}, fmt.Errorf("goal id is required")
	}
	title := goal.Title
	plan := Plan{
		GoalID:  goal.ID,
		Summary: fmt.Sprintf("A staged plan to deliver %q with requirements, design, implementation, and verification checkpoints.", title),
		Risks:   []string{"Requirements may change after inventory; review scope before committing to purchases or device changes."},
		Tasks: []TaskSpec{
			{Key: "requirements", Title: "Define requirements and success criteria", Description: "Clarify intended outcomes, budget, privacy constraints, and acceptance conditions for " + title + ".", SuccessCriteria: []string{"Requirements and acceptance criteria are recorded"}, EstimatedHours: 1.5},
			{Key: "inventory", Title: "Inventory existing environment", Description: "Document existing devices, network coverage, integrations, and constraints.", SuccessCriteria: []string{"Current devices and constraints are inventoried"}, DependsOn: []string{"requirements"}, EstimatedHours: 2, Risks: []string{"Inventory may contain sensitive network or device details; store it privately."}},
			{Key: "architecture", Title: "Design a phased architecture", Description: "Select integration boundaries and a phased architecture consistent with documented requirements.", SuccessCriteria: []string{"Architecture and selected integration boundaries are documented"}, DependsOn: []string{"requirements", "inventory"}, EstimatedHours: 3},
			{Key: "roadmap", Title: "Create implementation roadmap and estimates", Description: "Break the architecture into independently reviewable phases with dependencies and cost ranges.", SuccessCriteria: []string{"Ordered phases, dependencies, and estimates are prepared"}, DependsOn: []string{"architecture"}, EstimatedHours: 2},
			{Key: "safety-review", Title: "Review privacy, security, and safety risks", Description: "Review network exposure, credentials, data handling, failure behavior, and approval needs before any side effects.", SuccessCriteria: []string{"Risks and mitigations are reviewed by the owner"}, DependsOn: []string{"roadmap"}, EstimatedHours: 1.5},
		},
	}
	if len(goal.Criteria) > 0 {
		for _, criterion := range goal.Criteria {
			// The deterministic planner assigns the acceptance contract to its
			// requirements checkpoint; this is a planning mapping, not evidence.
			// 中文：确定性 Planner 将验收契约映射到 requirements 阶段；这只是计划映射，并非完成证据。
			index := 0
			if criterion.Type == goaldomain.CriterionArtifactExists || criterion.Type == goaldomain.CriterionMarkdownStructure {
				index = len(plan.Tasks) - 1
			}
			plan.Tasks[index].CriterionIDs = append(plan.Tasks[index].CriterionIDs, criterion.ID)
			plan.Tasks[index].EvidenceRequirements = append(plan.Tasks[index].EvidenceRequirements, fmt.Sprintf("Collect Core-verifiable evidence for criterion %s: %s", criterion.ID, criterion.Name))
		}
	}
	for _, task := range plan.Tasks {
		plan.EstimatedHours += task.EstimatedHours
	}
	if err := plan.Validate(); err != nil {
		return Plan{}, fmt.Errorf("validate generated plan: %w", err)
	}
	return plan, nil
}
