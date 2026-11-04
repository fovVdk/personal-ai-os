package verification

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/source"
)

type sourceFixtureRepository struct {
	snapshot  source.Snapshot
	reference source.Reference
}

func (r sourceFixtureRepository) Create(context.Context, source.Snapshot) (source.Snapshot, error) {
	panic("not used")
}
func (r sourceFixtureRepository) Get(_ context.Context, ownerID, goalID, id string) (source.Snapshot, error) {
	if ownerID != r.snapshot.OwnerID || goalID != r.snapshot.GoalID || id != r.snapshot.ID {
		return source.Snapshot{}, source.ErrNotFound
	}
	return r.snapshot, nil
}
func (r sourceFixtureRepository) List(context.Context, string, string, int) ([]source.Snapshot, error) {
	return []source.Snapshot{r.snapshot}, nil
}
func (r sourceFixtureRepository) AddReference(context.Context, source.Reference) (source.Reference, error) {
	panic("not used")
}
func (r sourceFixtureRepository) ListReferences(_ context.Context, ownerID, goalID, artifactPath string) ([]source.Reference, error) {
	if ownerID != r.reference.OwnerID || goalID != r.reference.GoalID || artifactPath != r.reference.ArtifactPath {
		return nil, nil
	}
	return []source.Reference{r.reference}, nil
}

func TestArtifactVerifierRequiresPersistedSourceReferences(t *testing.T) {
	root := t.TempDir()
	const artifactPath = "report.md"
	raw := []byte("Project documentation confirms a modular controller and autonomous navigation.")
	quote := "modular controller and autonomous navigation"
	snapshot := source.Snapshot{
		ID: "snapshot-1", OwnerID: "owner-1", GoalID: "goal-1", TaskID: "task-1",
		OriginalURL: "https://research.example/project", FinalURL: "https://research.example/project",
		HTTPStatus: 200, MIMEType: "text/plain", RetrievedAt: time.Now().UTC(), ContentHash: source.Hash(raw),
		RawContent: raw, NormalizedContent: string(raw), FetcherVersion: source.FetcherVersion,
	}
	reference := source.Reference{
		ID: "reference-1", OwnerID: snapshot.OwnerID, GoalID: snapshot.GoalID, TaskID: snapshot.TaskID,
		SnapshotID: snapshot.ID, ArtifactPath: artifactPath, CitationURL: snapshot.OriginalURL,
		Quote: quote, QuoteHash: source.Hash([]byte(quote)),
	}
	report := "# Research\n\n" + quote + ". [Source](source://" + snapshot.ID + ") " + snapshot.OriginalURL + "\n"
	if err := os.WriteFile(filepath.Join(root, artifactPath), []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier, err := NewArtifactVerifier(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	verifier.Sources = sourceFixtureRepository{snapshot: snapshot, reference: reference}
	request := agentruntime.Request{
		OwnerID: snapshot.OwnerID, GoalID: snapshot.GoalID, TaskID: snapshot.TaskID,
		TaskCriterionIDs: []string{"report"}, GoalCriteria: []goal.SuccessCriterion{{
			ID: "report", Version: 1, Required: true, Confirmed: true,
			Type: goal.CriterionMarkdownStructure, ValidationMethod: goal.CriterionMethodArtifactInStorage,
			ArtifactPath: artifactPath, RequiredSections: []string{"Research"}, MaxArtifactBytes: 1 << 20,
			RequireReference: true,
		}},
	}
	result, err := verifier.Verify(context.Background(), request, agentruntime.Result{})
	if err != nil || !result.Verified || len(result.Evidence) != 2 || result.Evidence[0].Source != "core.source-reference" {
		t.Fatalf("artifact/reference verification=%+v err=%v", result, err)
	}
	if result.Evidence[0].VerifierType != "core.source-reference" || result.Evidence[0].ContentHash != snapshot.ContentHash {
		t.Fatalf("source evidence is not hash-bound: %+v", result.Evidence[0])
	}
	if err := os.WriteFile(filepath.Join(root, artifactPath), []byte("# Research\nunsupported prose\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(context.Background(), request, agentruntime.Result{}); err == nil {
		t.Fatal("artifact without visible source citation unexpectedly verified")
	}
}
