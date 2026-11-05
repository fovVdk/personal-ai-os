// English: This opt-in PostgreSQL test captures one operator-selected public HTTPS source.
// 中文：本显式启用的 PostgreSQL 测试采集一个由运维选定的公开 HTTPS 来源。
package integration

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/source"
	"personal-ai-os/internal/task"
	"personal-ai-os/migrations"
)

// TestPostgresLiveSourceCapture is opt-in. It requires an operator-selected HTTPS URL,
// an exact host allowlist, and the disposable PostgreSQL test database.
// 中文：TestPostgresLiveSourceCapture 默认不运行；必须显式提供运维选定的 HTTPS URL、精确主机白名单和一次性 PostgreSQL 测试库。
func TestPostgresLiveSourceCapture(t *testing.T) {
	liveURL := strings.TrimSpace(os.Getenv("PAIOS_LIVE_SOURCE_URL"))
	allowlistValue := strings.TrimSpace(os.Getenv("PAIOS_LIVE_SOURCE_ALLOWED_HOSTS"))
	if liveURL == "" || allowlistValue == "" {
		t.Skip("BLOCKED: set PAIOS_LIVE_SOURCE_URL and PAIOS_LIVE_SOURCE_ALLOWED_HOSTS to explicitly enable live network acceptance")
	}
	parsed, err := url.ParseRequestURI(liveURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		t.Fatalf("live source URL must be an absolute credential-free HTTPS URL")
	}
	allowedHosts := make([]string, 0)
	allowed := make(map[string]bool)
	for _, host := range strings.Split(allowlistValue, ",") {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || strings.ContainsAny(host, "/:@*\\\r\n\x00") {
			t.Fatalf("live source allowlist entries must be exact hostnames")
		}
		allowedHosts = append(allowedHosts, host)
		allowed[host] = true
	}
	if !allowed[strings.ToLower(parsed.Hostname())] {
		t.Fatalf("live source hostname %q is not an exact member of PAIOS_LIVE_SOURCE_ALLOWED_HOSTS", parsed.Hostname())
	}
	fetcher, err := source.NewFetcher(allowedHosts)
	if err != nil {
		t.Fatalf("configure allowlisted source Fetcher: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	// Validate the explicitly disposable PostgreSQL test DB before any external request is made.
	// 中文：进行任何外部网络请求前，先确认连接的是明确的一次性 PostgreSQL 测试库。
	pool, repository := integrationDB(t)
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	snapshot, err := fetcher.Fetch(ctx, liveURL)
	if err != nil {
		t.Fatalf("BLOCKED: live source acquisition failed: %v", err)
	}
	if snapshot.HTTPStatus < 200 || snapshot.HTTPStatus >= 300 || snapshot.ContentHash != source.Hash(snapshot.RawContent) || len(snapshot.RawContent) == 0 {
		t.Fatalf("live source response failed status/content/hash verification: status=%d bytes=%d hash=%s", snapshot.HTTPStatus, len(snapshot.RawContent), snapshot.ContentHash)
	}
	finalURL, err := url.Parse(snapshot.FinalURL)
	if err != nil || finalURL.Scheme != "https" || !allowed[strings.ToLower(finalURL.Hostname())] {
		t.Fatalf("live source final URL escaped the configured HTTPS host allowlist: %q", snapshot.FinalURL)
	}
	if snapshot.RetrievedAt.IsZero() || time.Since(snapshot.RetrievedAt) > time.Minute || snapshot.RetrievedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("live source retrieval timestamp is invalid: %s", snapshot.RetrievedAt)
	}

	goalItem, err := repository.Create(ctx, goal.Goal{OwnerID: testOwnerID, Title: uniqueLabel("live-source-goal"), Priority: 3, Status: goal.Active})
	if err != nil {
		t.Fatalf("create live source Goal: %v", err)
	}
	taskItem, err := repository.Tasks().Create(ctx, task.Task{GoalID: goalItem.ID, Title: uniqueLabel("live-source-task"), Priority: 2, Status: task.Ready})
	if err != nil {
		t.Fatalf("create live source Task: %v", err)
	}
	goalIDs, taskIDs := []string{goalItem.ID}, []string{taskItem.ID}
	t.Cleanup(func() { cleanupIntegrationRows(t, context.Background(), pool, goalIDs, taskIDs, nil, nil, nil, nil) })
	snapshot.OwnerID, snapshot.GoalID, snapshot.TaskID = testOwnerID, goalItem.ID, taskItem.ID
	persisted, err := repository.Sources().Create(ctx, snapshot)
	if err != nil {
		t.Fatalf("persist live source snapshot: %v", err)
	}
	quote := firstRunes(persisted.NormalizedContent, 160)
	if strings.TrimSpace(quote) == "" {
		t.Fatal("live source normalized text is empty; cannot create a verifiable citation")
	}
	if _, err := repository.Sources().AddReference(ctx, source.Reference{
		OwnerID: testOwnerID, GoalID: goalItem.ID, TaskID: taskItem.ID,
		SnapshotID: persisted.ID, ArtifactPath: "reports/live-source.md", CitationURL: persisted.FinalURL,
		Quote: quote, QuoteHash: source.Hash([]byte(strings.Join(strings.Fields(quote), " "))),
	}); err != nil {
		t.Fatalf("bind live source citation to exact snapshot quotation: %v", err)
	}
	results, err := (source.Verifier{Repository: repository.Sources()}).Verify(ctx, testOwnerID, goalItem.ID, "reports/live-source.md", []source.ReferenceInput{{
		SnapshotID: persisted.ID, CitationURL: persisted.FinalURL, Quote: quote,
		QuoteHash: source.Hash([]byte(strings.Join(strings.Fields(quote), " "))),
	}})
	if err != nil || len(results) != 1 || !results[0].Verified || results[0].SemanticSupport != "requires_independent_review" {
		t.Fatalf("persisted live citation verification=%+v err=%v", results, err)
	}
	t.Logf("LIVE SOURCE CAPTURE VERIFIED: source_id=%s host=%s status=%d final_url=%s retrieved_at=%s content_sha256=%s bytes=%d", persisted.ID, finalURL.Hostname(), persisted.HTTPStatus, persisted.FinalURL, persisted.RetrievedAt.Format(time.RFC3339), persisted.ContentHash, len(persisted.RawContent))
}

func firstRunes(value string, count int) string {
	runes := []rune(value)
	if len(runes) > count {
		runes = runes[:count]
	}
	return string(runes)
}
