// Package source stores immutable external-source snapshots and verifies report references against them.
// 中文：source 包保存不可变外部来源快照，并基于快照核验报告引用。
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const FetcherVersion = "paios-safe-fetch/v1"

var ErrNotFound = errors.New("source snapshot not found")

// Snapshot is an immutable capture. RawContent and normalized content are retained for audit/reverification.
// 中文：Snapshot 是不可变来源采集记录；原始内容和规范化内容均保留，以便审计与重新验证。
type Snapshot struct {
	ID                string            `json:"source_id"`
	OwnerID           string            `json:"owner_id"`
	GoalID            string            `json:"goal_id"`
	TaskID            string            `json:"task_id"`
	OriginalURL       string            `json:"original_url"`
	FinalURL          string            `json:"final_url"`
	HTTPStatus        int               `json:"http_status"`
	MIMEType          string            `json:"mime_type"`
	RetrievedAt       time.Time         `json:"retrieved_at"`
	ContentHash       string            `json:"content_hash"`
	RawContent        []byte            `json:"-"`
	NormalizedContent string            `json:"normalized_content"`
	FetcherVersion    string            `json:"fetcher_version"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
}

func (s Snapshot) Validate() error {
	if s.OwnerID == "" || s.GoalID == "" || s.TaskID == "" || s.HTTPStatus < 100 || s.HTTPStatus > 599 || s.RetrievedAt.IsZero() || s.FetcherVersion == "" {
		return errors.New("source snapshot is missing required provenance fields")
	}
	if err := validateURL(s.OriginalURL); err != nil {
		return fmt.Errorf("invalid original URL: %w", err)
	}
	if err := validateURL(s.FinalURL); err != nil {
		return fmt.Errorf("invalid final URL: %w", err)
	}
	if !allowedMIME(strings.ToLower(s.MIMEType)) {
		return errors.New("source snapshot MIME type is not allowed")
	}
	if s.ContentHash != Hash(s.RawContent) {
		return errors.New("source snapshot content hash does not match raw content")
	}
	if s.NormalizedContent != NormalizeContent(strings.ToLower(s.MIMEType), s.RawContent) {
		return errors.New("source snapshot normalized content does not match raw content")
	}
	if len(s.RawContent) > MaxBodyBytes || len(s.NormalizedContent) > MaxBodyBytes {
		return errors.New("source snapshot exceeds content limit")
	}
	return nil
}

const MaxBodyBytes = 2 << 20
const DefaultSnapshotMaxAge = 30 * 24 * time.Hour

func Hash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func validateURL(raw string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("URL must be absolute HTTP(S) without credentials")
	}
	if strings.ContainsAny(raw, "\r\n\x00") {
		return errors.New("URL contains invalid characters")
	}
	return nil
}

type Reference struct {
	ID           string    `json:"reference_id"`
	OwnerID      string    `json:"owner_id"`
	GoalID       string    `json:"goal_id"`
	TaskID       string    `json:"task_id"`
	SnapshotID   string    `json:"source_id"`
	ArtifactPath string    `json:"artifact_path"`
	CitationURL  string    `json:"citation_url"`
	Quote        string    `json:"quote"`
	QuoteHash    string    `json:"quote_hash"`
	CreatedAt    time.Time `json:"created_at"`
}

type ReferenceInput struct {
	SnapshotID  string `json:"source_id"`
	CitationURL string `json:"url"`
	Quote       string `json:"quote"`
	QuoteHash   string `json:"quote_hash"`
}

type ReferenceResult struct {
	SnapshotID          string `json:"source_id"`
	URLPresent          bool   `json:"url_present"`
	SnapshotPersisted   bool   `json:"snapshot_persisted"`
	FetchSucceeded      bool   `json:"fetch_succeeded"`
	ContentHashVerified bool   `json:"content_hash_verified"`
	SnapshotFresh       bool   `json:"snapshot_fresh"`
	QuoteLinked         bool   `json:"quote_linked"`
	SemanticSupport     string `json:"semantic_support"`
	Verified            bool   `json:"verified"`
	Reason              string `json:"reason,omitempty"`
}

type Repository interface {
	Create(context.Context, Snapshot) (Snapshot, error)
	Get(context.Context, string, string, string) (Snapshot, error)
	List(context.Context, string, string, int) ([]Snapshot, error)
	AddReference(context.Context, Reference) (Reference, error)
	ListReferences(context.Context, string, string, string) ([]Reference, error)
}

// Verifier checks structural and lexical linkage only. It never asserts semantic support.
// 中文：Verifier 仅检查结构和词面关联；绝不声称来源在语义上支持报告陈述。
type Verifier struct {
	Repository Repository
	MaxAge     time.Duration
	Now        func() time.Time
}

func (v Verifier) Verify(ctx context.Context, ownerID, goalID, artifactPath string, inputs []ReferenceInput) ([]ReferenceResult, error) {
	if v.Repository == nil || ownerID == "" || goalID == "" || artifactPath == "" || len(inputs) == 0 {
		return nil, errors.New("reference verifier requires repository, Goal, artifact path and references")
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	maxAge := v.MaxAge
	if maxAge <= 0 {
		maxAge = DefaultSnapshotMaxAge
	}
	results := make([]ReferenceResult, 0, len(inputs))
	for _, input := range inputs {
		result := ReferenceResult{SnapshotID: input.SnapshotID, SemanticSupport: "requires_independent_review"}
		snapshot, err := v.Repository.Get(ctx, ownerID, goalID, input.SnapshotID)
		if errors.Is(err, ErrNotFound) {
			result.Reason = "snapshot_not_found"
			results = append(results, result)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load source snapshot %s: %w", input.SnapshotID, err)
		}
		result.SnapshotPersisted = true
		if err := snapshot.Validate(); err != nil {
			result.Reason = "snapshot_integrity_check_failed"
			results = append(results, result)
			continue
		}
		result.URLPresent = input.CitationURL != "" && (input.CitationURL == snapshot.OriginalURL || input.CitationURL == snapshot.FinalURL)
		result.FetchSucceeded = snapshot.HTTPStatus >= 200 && snapshot.HTTPStatus < 300
		result.ContentHashVerified = snapshot.ContentHash == Hash(snapshot.RawContent)
		result.SnapshotFresh = !snapshot.RetrievedAt.IsZero() && now().Sub(snapshot.RetrievedAt) >= 0 && now().Sub(snapshot.RetrievedAt) <= maxAge
		quote := normalizeQuote(input.Quote)
		result.QuoteLinked = quote != "" && input.QuoteHash == Hash([]byte(quote)) && strings.Contains(snapshot.NormalizedContent, quote)
		result.Verified = result.URLPresent && result.FetchSucceeded && result.ContentHashVerified && result.SnapshotFresh && result.QuoteLinked
		if !result.Verified {
			result.Reason = "reference_structure_or_snapshot_check_failed"
		}
		results = append(results, result)
	}
	return results, nil
}

func normalizeQuote(value string) string { return strings.Join(strings.Fields(value), " ") }
