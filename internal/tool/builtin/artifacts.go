// English: This file implements criterion-scoped artifact writing, exact-approval report export, and snapshot citation binding.
// 中文：本文件实现按成功标准限定的产物写入、精确审批报告导出及来源快照引用绑定。
package builtin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/source"
	"personal-ai-os/internal/task"
	"personal-ai-os/internal/tool/registry"
)

const maxArtifactToolBytes = 2 << 20

type artifactWriter struct {
	root  fileRoot
	goals goal.Repository
	tasks task.Repository
	owner string
}

type reportExporter struct {
	reader  artifactWriter
	export  fileRoot
	maxSize int64
}

func (h reportExporter) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("report export requires a Gateway-verified Goal and Task context")
}

func (h reportExporter) ExecuteWithContext(ctx context.Context, invocation registry.InvocationContext, args json.RawMessage) (json.RawMessage, error) {
	var input struct {
		CriterionID      string `json:"criterion_id"`
		CriterionVersion int    `json:"criterion_version"`
		ArtifactPath     string `json:"artifact_path"`
		SHA256           string `json:"sha256"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, err
	}
	if len(input.SHA256) != 64 || strings.ToLower(input.SHA256) != input.SHA256 {
		return nil, errors.New("report export requires the exact lowercase SHA-256 accepted for approval")
	}
	criterion, err := h.reader.authorizedCriterion(ctx, invocation.GoalID, invocation.TaskID, input.CriterionID)
	if err != nil {
		return nil, err
	}
	if input.CriterionVersion != criterion.Version || input.ArtifactPath != criterion.ArtifactPath {
		return nil, errors.New("report target or criterion version changed after exact export parameters were approved")
	}
	name := filepath.Clean(criterion.ArtifactPath)
	if !filepath.IsLocal(name) || name == "." || filepath.IsAbs(name) || strings.ContainsAny(name, "\\\x00\r\n") {
		return nil, errors.New("persisted report criterion has an unsafe path")
	}
	sourceRoot, err := os.OpenRoot(h.reader.root.root)
	if err != nil {
		return nil, fmt.Errorf("open verified Artifact root: %w", err)
	}
	defer sourceRoot.Close()
	file, err := sourceRoot.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open report for approved export: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat report for approved export: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > h.maxSize {
		_ = file.Close()
		return nil, errors.New("report is not a regular file or exceeds export limit")
	}
	content, err := io.ReadAll(io.LimitReader(file, h.maxSize+1))
	closeErr := file.Close()
	if err != nil {
		return nil, fmt.Errorf("read report for approved export: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close report for approved export: %w", closeErr)
	}
	if int64(len(content)) > h.maxSize || source.Hash(content) != input.SHA256 {
		return nil, errors.New("report bytes changed after the exact export parameters were approved")
	}
	exportRoot, err := os.OpenRoot(h.export.root)
	if err != nil {
		return nil, fmt.Errorf("open configured report export root: %w", err)
	}
	defer exportRoot.Close()
	if err := atomicRootWrite(ctx, exportRoot, name, content, true); err != nil {
		return nil, fmt.Errorf("publish approved report into configured export root: %w", err)
	}
	return resultJSON(fmt.Sprintf("published://%s#sha256=%s", filepath.ToSlash(name), input.SHA256))
}

func (h artifactWriter) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("artifact writing requires a Gateway-verified Goal and Task context")
}

func (h artifactWriter) ExecuteWithContext(ctx context.Context, invocation registry.InvocationContext, args json.RawMessage) (json.RawMessage, error) {
	if invocation.GoalID == "" || invocation.TaskID == "" || invocation.ExecutionID == "" || invocation.StepID == "" {
		return nil, errors.New("artifact writing requires verified Goal, Task, Execution and Step identifiers")
	}
	var input struct {
		CriterionID string `json:"criterion_id"`
		Content     string `json:"content"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, err
	}
	if input.CriterionID == "" || len(input.Content) == 0 || len(input.Content) > maxArtifactToolBytes || !utf8.ValidString(input.Content) {
		return nil, errors.New("artifact criterion and valid UTF-8 content within the write limit are required")
	}
	criterion, err := h.authorizedCriterion(ctx, invocation.GoalID, invocation.TaskID, input.CriterionID)
	if err != nil {
		return nil, err
	}
	limit := criterion.MaxArtifactBytes
	if limit <= 0 || limit > maxArtifactToolBytes {
		limit = maxArtifactToolBytes
	}
	if int64(len(input.Content)) > limit {
		return nil, fmt.Errorf("artifact exceeds criterion size limit of %d bytes", limit)
	}
	if criterion.Type == goal.CriterionMarkdownStructure && filepath.Ext(criterion.ArtifactPath) != ".md" {
		return nil, errors.New("Markdown artifact criterion must target a .md file")
	}
	name := filepath.Clean(criterion.ArtifactPath)
	if !filepath.IsLocal(name) || name == "." || filepath.IsAbs(name) || strings.ContainsAny(name, "\\\x00\r\n") {
		return nil, errors.New("persisted artifact criterion has an unsafe path")
	}
	root, err := os.OpenRoot(h.root.root)
	if err != nil {
		return nil, fmt.Errorf("open configured artifact root: %w", err)
	}
	defer root.Close()
	if err := atomicRootWrite(ctx, root, name, []byte(input.Content), false); err != nil {
		return nil, fmt.Errorf("atomically write confirmed artifact: %w", err)
	}
	hash := source.Hash([]byte(input.Content))
	return resultJSON(fmt.Sprintf("artifact://%s#sha256=%s", filepath.ToSlash(name), hash))
}

func atomicRootWrite(ctx context.Context, root *os.Root, name string, content []byte, noReplace bool) error {
	parent := filepath.Dir(name)
	if parent != "." {
		if err := root.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("create directory beneath trusted root: %w", err)
		}
	}
	if noReplace {
		info, err := root.Lstat(name)
		if err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("report export target exists and is not a regular file")
			}
			existing, err := root.ReadFile(name)
			if err != nil {
				return fmt.Errorf("read existing report export target: %w", err)
			}
			if bytes.Equal(existing, content) {
				return nil
			}
			return errors.New("report export target already contains different bytes; refusing to overwrite")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect report export target: %w", err)
		}
	}
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Errorf("generate artifact staging name: %w", err)
	}
	temporary := filepath.Join(parent, ".paios-artifact-"+hex.EncodeToString(suffix[:])+".tmp")
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create artifact staging file: %w", err)
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = root.Remove(temporary)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("write artifact staging file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync artifact staging file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close artifact staging file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if noReplace {
		if err := root.Link(temporary, name); err != nil {
			return fmt.Errorf("atomically publish without replacing an existing target: %w", err)
		}
		if err := root.Remove(temporary); err != nil {
			return fmt.Errorf("remove report export staging link: %w", err)
		}
	} else if err := root.Rename(temporary, name); err != nil {
		return fmt.Errorf("atomically commit artifact beneath storage root: %w", err)
	}
	committed = true
	return nil
}

func (h artifactWriter) authorizedCriterion(ctx context.Context, goalID, taskID, criterionID string) (goal.SuccessCriterion, error) {
	goalItem, err := h.goals.Get(ctx, h.owner, goalID)
	if err != nil {
		return goal.SuccessCriterion{}, fmt.Errorf("load artifact Goal: %w", err)
	}
	taskItem, err := h.tasks.Get(ctx, taskID)
	if err != nil {
		return goal.SuccessCriterion{}, fmt.Errorf("load artifact Task: %w", err)
	}
	if taskItem.GoalID != goalItem.ID {
		return goal.SuccessCriterion{}, errors.New("artifact Task does not belong to the invocation Goal")
	}
	if taskItem.Status != task.Running {
		return goal.SuccessCriterion{}, errors.New("artifact operation requires a currently running durable Task")
	}
	mapped := false
	for _, id := range taskItem.CriterionIDs {
		if id == criterionID {
			mapped = true
			break
		}
	}
	if !mapped {
		return goal.SuccessCriterion{}, errors.New("artifact Task is not mapped to the requested criterion")
	}
	for _, criterion := range goalItem.Criteria {
		if criterion.ID != criterionID {
			continue
		}
		if !criterion.Required || !criterion.Confirmed || criterion.ValidationMethod != goal.CriterionMethodArtifactInStorage || (criterion.Type != goal.CriterionArtifactExists && criterion.Type != goal.CriterionMarkdownStructure) {
			return goal.SuccessCriterion{}, errors.New("artifact criterion is not a confirmed required file criterion")
		}
		return criterion, nil
	}
	return goal.SuccessCriterion{}, errors.New("artifact criterion was not found on the persisted Goal")
}

type sourceReferenceTool struct {
	goals      goal.Repository
	tasks      task.Repository
	repository source.Repository
	owner      string
}

func (h sourceReferenceTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("source reference registration requires a Gateway-verified Goal and Task context")
}

func (h sourceReferenceTool) ExecuteWithContext(ctx context.Context, invocation registry.InvocationContext, args json.RawMessage) (json.RawMessage, error) {
	if invocation.GoalID == "" || invocation.TaskID == "" || invocation.ExecutionID == "" || invocation.StepID == "" {
		return nil, errors.New("source reference registration requires verified Goal, Task, Execution and Step identifiers")
	}
	var input struct {
		CriterionID string `json:"criterion_id"`
		SnapshotID  string `json:"source_id"`
		URL         string `json:"url"`
		Quote       string `json:"quote"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, err
	}
	writer := artifactWriter{goals: h.goals, tasks: h.tasks, owner: h.owner}
	criterion, err := writer.authorizedCriterion(ctx, invocation.GoalID, invocation.TaskID, input.CriterionID)
	if err != nil {
		return nil, err
	}
	if !criterion.RequireReference {
		return nil, errors.New("artifact criterion does not require source references")
	}
	quote := strings.Join(strings.Fields(input.Quote), " ")
	if quote == "" {
		return nil, errors.New("source quote is required")
	}
	reference, err := h.repository.AddReference(ctx, source.Reference{
		OwnerID: h.owner, GoalID: invocation.GoalID, TaskID: invocation.TaskID,
		SnapshotID: input.SnapshotID, ArtifactPath: criterion.ArtifactPath,
		CitationURL: input.URL, Quote: quote, QuoteHash: source.Hash([]byte(quote)),
	})
	if err != nil {
		return nil, fmt.Errorf("persist Goal-scoped source reference: %w", err)
	}
	encoded, err := json.Marshal(map[string]string{"reference_id": reference.ID, "source_id": reference.SnapshotID, "artifact_path": reference.ArtifactPath, "url": reference.CitationURL, "quote_hash": reference.QuoteHash})
	if err != nil {
		return nil, err
	}
	return resultJSON(string(encoded))
}
