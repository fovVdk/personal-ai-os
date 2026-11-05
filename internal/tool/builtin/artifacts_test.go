// English: These tests verify criterion-scoped atomic artifact writes, Task/Goal authorization, and source binding.
// 中文：本测试验证成功标准范围内的原子产物写入、Task/Goal 授权及来源绑定。
package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/source"
	"personal-ai-os/internal/task"
	"personal-ai-os/internal/tool/registry"
)

type artifactGoalRepository struct{ item goal.Goal }

func (r artifactGoalRepository) Create(context.Context, goal.Goal) (goal.Goal, error) {
	return goal.Goal{}, errors.New("not implemented")
}
func (r artifactGoalRepository) Get(_ context.Context, ownerID, id string) (goal.Goal, error) {
	if ownerID != r.item.OwnerID || id != r.item.ID {
		return goal.Goal{}, errors.New("Goal not found")
	}
	return r.item, nil
}
func (r artifactGoalRepository) List(context.Context, string, int) ([]goal.Goal, error) {
	return nil, errors.New("not implemented")
}
func (r artifactGoalRepository) Update(context.Context, string, goal.Goal) (goal.Goal, error) {
	return goal.Goal{}, errors.New("not implemented")
}
func (r artifactGoalRepository) Transition(context.Context, string, string, goal.Status, []goal.Evidence) (goal.Goal, error) {
	return goal.Goal{}, errors.New("not implemented")
}

type artifactTaskRepository struct{ item task.Task }

func (r artifactTaskRepository) Create(context.Context, task.Task) (task.Task, error) {
	return task.Task{}, errors.New("not implemented")
}
func (r artifactTaskRepository) CreateMany(context.Context, []task.Task) ([]task.Task, error) {
	return nil, errors.New("not implemented")
}
func (r artifactTaskRepository) Get(_ context.Context, id string) (task.Task, error) {
	if id != r.item.ID {
		return task.Task{}, errors.New("Task not found")
	}
	return r.item, nil
}
func (r artifactTaskRepository) ListByGoal(context.Context, string, int) ([]task.Task, error) {
	return nil, errors.New("not implemented")
}
func (r artifactTaskRepository) Transition(context.Context, string, task.Status) (task.Task, error) {
	return task.Task{}, errors.New("not implemented")
}

type referenceRepository struct {
	added source.Reference
}

func (r *referenceRepository) Create(context.Context, source.Snapshot) (source.Snapshot, error) {
	return source.Snapshot{}, errors.New("not implemented")
}
func (r *referenceRepository) Get(context.Context, string, string, string) (source.Snapshot, error) {
	return source.Snapshot{}, source.ErrNotFound
}
func (r *referenceRepository) List(context.Context, string, string, int) ([]source.Snapshot, error) {
	return nil, nil
}
func (r *referenceRepository) AddReference(_ context.Context, item source.Reference) (source.Reference, error) {
	item.ID = "reference-id"
	r.added = item
	return item, nil
}
func (r *referenceRepository) ListReferences(context.Context, string, string, string) ([]source.Reference, error) {
	return nil, nil
}

func artifactToolFixture(root string) (artifactWriter, registry.InvocationContext) {
	ownerID, goalID, taskID, criterionID := "owner-id", "goal-id", "task-id", "report"
	goalRepo := artifactGoalRepository{item: goal.Goal{
		ID: goalID, OwnerID: ownerID, Status: goal.Active,
		Criteria: []goal.SuccessCriterion{{
			ID: criterionID, Name: "Research report", Type: goal.CriterionMarkdownStructure, Required: true,
			ValidationMethod: goal.CriterionMethodArtifactInStorage, ArtifactPath: "reports/final.md",
			RequiredSections: []string{"Summary", "Sources"}, MaxArtifactBytes: 4096,
			RequireReference: true, Source: goal.CriterionSourceUser, Confirmed: true, Version: 1,
		}},
	}}
	taskRepo := artifactTaskRepository{item: task.Task{ID: taskID, GoalID: goalID, Status: task.Running, CriterionIDs: []string{criterionID}}}
	rootItem, err := newFileRoot(root)
	if err != nil {
		panic(err)
	}
	return artifactWriter{root: rootItem, goals: goalRepo, tasks: taskRepo, owner: ownerID}, registry.InvocationContext{GoalID: goalID, TaskID: taskID, ExecutionID: "execution-id", StepID: "step-id", AgentID: "research"}
}

func TestArtifactWriterUsesOnlyConfirmedCriterionPathAndAtomicRootWrite(t *testing.T) {
	root := t.TempDir()
	writer, invocation := artifactToolFixture(root)
	content := "# Summary\nA bounded report.\n## Sources\nEvidence citation.\n"
	arguments, _ := json.Marshal(map[string]string{"criterion_id": "report", "content": content})
	if _, err := writer.Execute(context.Background(), arguments); err == nil {
		t.Fatal("artifact writer accepted a call without Gateway invocation context")
	}
	output, err := writer.ExecuteWithContext(context.Background(), invocation, arguments)
	if err != nil {
		t.Fatalf("write confirmed report artifact: %v", err)
	}
	actual, err := os.ReadFile(filepath.Join(root, "reports/final.md"))
	if err != nil || string(actual) != content {
		t.Fatalf("written report=%q err=%v", actual, err)
	}
	var result struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(output, &result); err != nil || result.Result == "" {
		t.Fatalf("artifact write result=%s err=%v", output, err)
	}
	for _, invalidCriterion := range []string{"../../outside", "unmapped"} {
		args, _ := json.Marshal(map[string]string{"criterion_id": invalidCriterion, "content": "# overwrite"})
		if _, err := writer.ExecuteWithContext(context.Background(), invocation, args); err == nil {
			t.Errorf("writer accepted unauthorized criterion id %q", invalidCriterion)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected artifact outside criterion path: %v", err)
	}
}

func TestArtifactWriterRejectsCrossGoalTaskAndCanceledWrite(t *testing.T) {
	root := t.TempDir()
	writer, invocation := artifactToolFixture(root)
	arguments, _ := json.Marshal(map[string]string{"criterion_id": "report", "content": "# Summary\n## Sources\n"})
	invocation.GoalID = "other-goal"
	if _, err := writer.ExecuteWithContext(context.Background(), invocation, arguments); err == nil {
		t.Fatal("artifact writer accepted a Task belonging to a different Goal")
	}
	invocation.GoalID = "goal-id"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := writer.ExecuteWithContext(ctx, invocation, arguments); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled artifact write error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "reports/final.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled operation committed a report: %v", err)
	}
}

func TestSourceReferenceToolDerivesPathFromPersistedCriterion(t *testing.T) {
	root := t.TempDir()
	writer, invocation := artifactToolFixture(root)
	repository := &referenceRepository{}
	handler := sourceReferenceTool{goals: writer.goals, tasks: writer.tasks, repository: repository, owner: writer.owner}
	arguments, _ := json.Marshal(map[string]string{
		"criterion_id": "report", "source_id": "snapshot-id", "url": "https://research.example/project",
		"quote": "  exact   source quote  ",
	})
	if _, err := handler.Execute(context.Background(), arguments); err == nil {
		t.Fatal("source reference Tool accepted a call without Gateway context")
	}
	if _, err := handler.ExecuteWithContext(context.Background(), invocation, arguments); err != nil {
		t.Fatalf("register source reference: %v", err)
	}
	if repository.added.ArtifactPath != "reports/final.md" || repository.added.SnapshotID != "snapshot-id" || repository.added.Quote != "exact source quote" || repository.added.QuoteHash != source.Hash([]byte("exact source quote")) {
		t.Fatalf("reference binding did not derive trusted path/quote: %+v", repository.added)
	}
}
