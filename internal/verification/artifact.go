// Package verification contains Core-owned deterministic result verifiers.
// 中文：verification 包实现由 Core 控制的确定性结果验证器。
package verification

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	agentruntime "personal-ai-os/internal/agent/runtime"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/source"
)

const ArtifactVerifierType = "core.file-artifact"
const ArtifactVerifierVersion = "v1"

// ArtifactVerifier reads only explicitly named files under a trusted storage root.
// It never trusts a ModelAgent-provided summary or Evidence as proof.
// 中文：ArtifactVerifier 只读取受信存储根目录内标准显式指定的文件，不信任模型摘要或自报 Evidence。
type ArtifactVerifier struct {
	Root         string
	DefaultLimit int64
	Sources      source.Repository
	SourceMaxAge time.Duration
	Now          func() time.Time
}

func NewArtifactVerifier(root string, defaultLimit int64) (*ArtifactVerifier, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("artifact storage root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact storage root: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("stat artifact storage root: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("artifact storage root is not a directory")
	}
	if defaultLimit < 1 || defaultLimit > 64<<20 {
		defaultLimit = 8 << 20
	}
	return &ArtifactVerifier{Root: absolute, DefaultLimit: defaultLimit}, nil
}

func (v *ArtifactVerifier) Verify(ctx context.Context, request agentruntime.Request, _ agentruntime.Result) (agentruntime.Verification, error) {
	if err := ctx.Err(); err != nil {
		return agentruntime.Verification{}, err
	}
	if v == nil || v.Root == "" {
		return agentruntime.Verification{}, errors.New("artifact verifier is not configured")
	}
	mapped := make(map[string]bool, len(request.TaskCriterionIDs))
	for _, id := range request.TaskCriterionIDs {
		mapped[id] = true
	}
	root, err := os.OpenRoot(v.Root)
	if err != nil {
		return agentruntime.Verification{}, fmt.Errorf("open artifact storage root: %w", err)
	}
	defer root.Close()
	verification := agentruntime.Verification{Method: ArtifactVerifierType + "/" + ArtifactVerifierVersion}
	for _, criterion := range request.GoalCriteria {
		if !mapped[criterion.ID] || !criterion.Required || !criterion.Confirmed {
			continue
		}
		if criterion.Type != goal.CriterionArtifactExists && criterion.Type != goal.CriterionMarkdownStructure {
			continue
		}
		if criterion.ValidationMethod != goal.CriterionMethodArtifactInStorage {
			continue
		}
		content, hash, err := v.readArtifact(root, criterion)
		if err != nil {
			return agentruntime.Verification{Method: verification.Method}, fmt.Errorf("verify artifact criterion %s: %w", criterion.ID, err)
		}
		if criterion.Type == goal.CriterionMarkdownStructure {
			if err := verifyMarkdownSections(content, criterion.RequiredSections); err != nil {
				return agentruntime.Verification{Method: verification.Method}, fmt.Errorf("verify Markdown criterion %s: %w", criterion.ID, err)
			}
		}
		if criterion.RequireReference {
			if v.Sources == nil || request.OwnerID == "" {
				return agentruntime.Verification{Method: verification.Method}, errors.New("source reference verification is not configured")
			}
			references, err := v.Sources.ListReferences(ctx, request.OwnerID, request.GoalID, criterion.ArtifactPath)
			if err != nil {
				return agentruntime.Verification{Method: verification.Method}, fmt.Errorf("load report source references: %w", err)
			}
			if len(references) == 0 {
				return agentruntime.Verification{Method: verification.Method}, errors.New("required report source references are missing")
			}
			referenceVerifier := source.Verifier{Repository: v.Sources, MaxAge: v.SourceMaxAge, Now: v.Now}
			inputs := make([]source.ReferenceInput, 0, len(references))
			for _, reference := range references {
				inputs = append(inputs, source.ReferenceInput{SnapshotID: reference.SnapshotID, CitationURL: reference.CitationURL, Quote: reference.Quote, QuoteHash: reference.QuoteHash})
			}
			results, err := referenceVerifier.Verify(ctx, request.OwnerID, request.GoalID, criterion.ArtifactPath, inputs)
			if err != nil {
				return agentruntime.Verification{Method: verification.Method}, fmt.Errorf("verify report source references: %w", err)
			}
			for index, result := range results {
				reference := references[index]
				if !result.Verified {
					return agentruntime.Verification{Method: verification.Method}, fmt.Errorf("report source reference %s is not verified: %s", reference.ID, result.Reason)
				}
				if !strings.Contains(string(content), "source://"+reference.SnapshotID) || !strings.Contains(string(content), reference.CitationURL) || !strings.Contains(string(content), reference.Quote) {
					return agentruntime.Verification{Method: verification.Method}, fmt.Errorf("report does not visibly cite source snapshot %s and its quoted content", reference.SnapshotID)
				}
				snapshot, err := v.Sources.Get(ctx, request.OwnerID, request.GoalID, reference.SnapshotID)
				if err != nil {
					return agentruntime.Verification{Method: verification.Method}, fmt.Errorf("reload cited source snapshot %s: %w", reference.SnapshotID, err)
				}
				verification.Evidence = append(verification.Evidence, agentruntime.Evidence{
					CriterionID: criterion.ID, CriterionVersion: criterion.Version,
					Source: "core.source-reference", Description: "Core verified URL, immutable snapshot hash, freshness and exact quote linkage; semantic claim support requires independent review",
					Reference: "source://" + snapshot.ID + "#sha256=" + snapshot.ContentHash, ContentHash: snapshot.ContentHash,
					VerifierType: "core.source-reference", VerifierVersion: "v1",
				})
			}
		}
		verification.Evidence = append(verification.Evidence, agentruntime.Evidence{
			CriterionID: criterion.ID, CriterionVersion: criterion.Version,
			Source:      ArtifactVerifierType,
			Description: fmt.Sprintf("Core verified file exists and passed %s validation (%d bytes)", criterion.Type, len(content)),
			Reference:   "artifact://" + filepath.ToSlash(criterion.ArtifactPath) + "#sha256=" + hash,
			ContentHash: hash, VerifierType: ArtifactVerifierType, VerifierVersion: ArtifactVerifierVersion,
		})
	}
	verification.Verified = len(verification.Evidence) > 0
	return verification, nil
}

func (v *ArtifactVerifier) readArtifact(root *os.Root, criterion goal.SuccessCriterion) ([]byte, string, error) {
	if !filepath.IsLocal(criterion.ArtifactPath) || filepath.IsAbs(criterion.ArtifactPath) {
		return nil, "", errors.New("artifact path must be a non-empty local relative path")
	}
	if strings.ContainsAny(criterion.ArtifactPath, "\\\x00\r\n") {
		return nil, "", errors.New("artifact path contains invalid characters")
	}
	name := filepath.Clean(criterion.ArtifactPath)
	if name == "." {
		return nil, "", errors.New("artifact path must identify a file")
	}
	limit := criterion.MaxArtifactBytes
	if limit <= 0 || limit > 64<<20 {
		limit = v.DefaultLimit
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, "", fmt.Errorf("open artifact within storage root: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, "", fmt.Errorf("stat artifact: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", errors.New("artifact is not a regular file")
	}
	if info.Size() > limit {
		return nil, "", fmt.Errorf("artifact size %d exceeds limit %d", info.Size(), limit)
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("read artifact: %w", err)
	}
	if int64(len(content)) > limit {
		return nil, "", fmt.Errorf("artifact exceeds limit %d", limit)
	}
	if criterion.Type == goal.CriterionMarkdownStructure {
		if filepath.Ext(name) != ".md" || !utf8.Valid(content) {
			return nil, "", errors.New("Markdown artifact must be UTF-8 with .md extension")
		}
	}
	digest := sha256.Sum256(content)
	return content, hex.EncodeToString(digest[:]), nil
}

func (v *ArtifactVerifier) VerifyArtifactHash(ctx context.Context, relativePath, expectedHash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if v == nil || v.Root == "" {
		return errors.New("artifact verifier is not configured")
	}
	root, err := os.OpenRoot(v.Root)
	if err != nil {
		return fmt.Errorf("open artifact storage root: %w", err)
	}
	defer root.Close()
	_, currentHash, err := v.readArtifact(root, goal.SuccessCriterion{ArtifactPath: relativePath, Type: goal.CriterionArtifactExists, MaxArtifactBytes: v.DefaultLimit})
	if err != nil {
		return err
	}
	if len(expectedHash) != 64 || currentHash != strings.ToLower(expectedHash) {
		return errors.New("artifact content hash changed after verification")
	}
	return nil
}

func verifyMarkdownSections(content []byte, required []string) error {
	found := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "#") {
			continue
		}
		title := strings.TrimSpace(strings.TrimLeft(line, "#"))
		found[strings.ToLower(title)] = true
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan Markdown headings: %w", err)
	}
	missing := make([]string, 0)
	for _, section := range required {
		if !found[strings.ToLower(strings.TrimSpace(section))] {
			missing = append(missing, section)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("required Markdown sections are missing: %s", strings.Join(missing, ", "))
	}
	return nil
}

var _ agentruntime.ResultVerifier = (*ArtifactVerifier)(nil)
