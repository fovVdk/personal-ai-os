package source

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"
	"time"
)

type memoryRepository struct{ snapshot Snapshot }

func (r memoryRepository) Create(context.Context, Snapshot) (Snapshot, error) {
	return Snapshot{}, errors.New("not implemented")
}
func (r memoryRepository) Get(_ context.Context, ownerID, goalID, id string) (Snapshot, error) {
	if ownerID != r.snapshot.OwnerID || goalID != r.snapshot.GoalID || id != r.snapshot.ID {
		return Snapshot{}, ErrNotFound
	}
	return r.snapshot, nil
}
func (r memoryRepository) List(context.Context, string, string, int) ([]Snapshot, error) {
	return []Snapshot{r.snapshot}, nil
}
func (r memoryRepository) AddReference(context.Context, Reference) (Reference, error) {
	return Reference{}, errors.New("not implemented")
}
func (r memoryRepository) ListReferences(context.Context, string, string, string) ([]Reference, error) {
	return nil, nil
}

func TestFetcherValidationAndNormalization(t *testing.T) {
	fetcher, err := NewFetcher([]string{"example.com"})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"http://127.0.0.1/", "https://example.com:8443/", "file:///etc/passwd", "https://user@example.com/"} {
		parsed, err := parseURL(target)
		if err != nil {
			continue
		}
		if err := fetcher.validateTarget(parsed); err == nil {
			t.Errorf("unsafe or unallowlisted target accepted: %s", target)
		}
	}
	if got := NormalizeContent("text/html", []byte("<h1>Title</h1><script>alert('no')</script><p>A &amp; B</p>")); got != "Title A & B" {
		t.Fatalf("HTML normalization=%q", got)
	}
	for _, mime := range []string{"text/plain", "text/html", "application/json", "application/xhtml+xml"} {
		if !allowedMIME(mime) {
			t.Errorf("allowed MIME rejected: %s", mime)
		}
	}
	for _, mime := range []string{"application/octet-stream", "application/javascript", "image/svg+xml"} {
		if allowedMIME(mime) {
			t.Errorf("active/binary MIME accepted: %s", mime)
		}
	}
}

func TestPublicIPRejectsLocalReservedAndDocumentationRanges(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "192.0.2.1", "198.18.0.1", "0.1.2.3", "240.1.1.1", "::ffff:127.0.0.1", "::1", "fc00::1", "fe80::1", "2001:20::1"} {
		if publicIP(net.ParseIP(value)) {
			t.Errorf("non-public IP accepted: %s", value)
		}
	}
	if !publicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address rejected")
	}
}

func TestReferenceVerifierSeparatesSnapshotFromSemanticSupport(t *testing.T) {
	raw := []byte("The device uses a modular controller and supports safe navigation.")
	snapshot := Snapshot{
		ID: "snapshot-1", OwnerID: "owner-1", GoalID: "goal-1", TaskID: "task-1",
		OriginalURL: "https://example.com/a", FinalURL: "https://example.com/a", HTTPStatus: 200,
		MIMEType: "text/plain", RetrievedAt: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC),
		RawContent: raw, ContentHash: Hash(raw), NormalizedContent: string(raw), FetcherVersion: FetcherVersion,
	}
	quote := "modular controller and supports safe navigation"
	input := ReferenceInput{SnapshotID: snapshot.ID, CitationURL: snapshot.FinalURL, Quote: quote, QuoteHash: Hash([]byte(quote))}
	verifier := Verifier{Repository: memoryRepository{snapshot: snapshot}, MaxAge: time.Hour, Now: func() time.Time { return snapshot.RetrievedAt.Add(time.Minute) }}
	results, err := verifier.Verify(context.Background(), snapshot.OwnerID, snapshot.GoalID, "report.md", []ReferenceInput{input})
	if err != nil || len(results) != 1 || !results[0].Verified || results[0].SemanticSupport != "requires_independent_review" {
		t.Fatalf("valid structural reference=%+v err=%v", results, err)
	}
	input.Quote = "unsupported claim"
	input.QuoteHash = Hash([]byte(input.Quote))
	results, err = verifier.Verify(context.Background(), snapshot.OwnerID, snapshot.GoalID, "report.md", []ReferenceInput{input})
	if err != nil || len(results) != 1 || results[0].Verified || results[0].QuoteLinked {
		t.Fatalf("fabricated quotation passed: %+v err=%v", results, err)
	}
	input.Quote = quote
	input.QuoteHash = Hash([]byte(quote))
	input.CitationURL = "https://other.example/"
	results, err = verifier.Verify(context.Background(), snapshot.OwnerID, snapshot.GoalID, "report.md", []ReferenceInput{input})
	if err != nil || len(results) != 1 || results[0].Verified || results[0].URLPresent {
		t.Fatalf("unbound URL passed: %+v err=%v", results, err)
	}
}

func parseURL(value string) (*url.URL, error) { return url.Parse(value) }
