// English: These tests validate controlled source acquisition, safety boundaries, and snapshot/reference provenance.
// 中文：本测试验证受控来源采集、安全边界及快照/引用来源记录。
package source

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type memoryRepository struct{ snapshot Snapshot }

type resolverFunc func(context.Context, string) ([]net.IPAddr, error)

func (f resolverFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f(ctx, host)
}

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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func responseFor(request *http.Request, status int, contentType, location string, body io.ReadCloser) *http.Response {
	header := make(http.Header)
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	if location != "" {
		header.Set("Location", location)
	}
	return &http.Response{StatusCode: status, Header: header, Body: body, Request: request}
}

func TestFetcherControlledTransportCapturesImmutableResponse(t *testing.T) {
	fetcher, err := NewFetcher([]string{"research.example"})
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	fetcher.now = func() time.Time { return fixed }
	const body = "# Research\nA source sentence with exact quotation."
	fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return responseFor(request, http.StatusOK, "text/plain; charset=utf-8", "", io.NopCloser(strings.NewReader(body))), nil
	})
	snapshot, err := fetcher.Fetch(context.Background(), "https://research.example/project")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	snapshot.OwnerID, snapshot.GoalID, snapshot.TaskID = "owner", "goal", "task"
	if snapshot.OriginalURL != "https://research.example/project" || snapshot.FinalURL != snapshot.OriginalURL || snapshot.HTTPStatus != http.StatusOK || snapshot.MIMEType != "text/plain" || !snapshot.RetrievedAt.Equal(fixed) {
		t.Fatalf("snapshot provenance=%+v", snapshot)
	}
	if string(snapshot.RawContent) != body || snapshot.ContentHash != Hash([]byte(body)) || snapshot.NormalizedContent != NormalizeContent("text/plain", []byte(body)) {
		t.Fatalf("snapshot content/hash mismatch: %+v", snapshot)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("captured snapshot failed validation: %v", err)
	}
}

func TestFetcherRedirectsRevalidateExactHostAllowlist(t *testing.T) {
	t.Run("allowed redirect", func(t *testing.T) {
		fetcher, err := NewFetcher([]string{"research.example", "mirror.example"})
		if err != nil {
			t.Fatal(err)
		}
		fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Hostname() == "research.example" {
				return responseFor(request, http.StatusFound, "text/plain", "https://mirror.example/final", io.NopCloser(strings.NewReader("redirect"))), nil
			}
			return responseFor(request, http.StatusOK, "text/plain", "", io.NopCloser(strings.NewReader("final content"))), nil
		})
		snapshot, err := fetcher.Fetch(context.Background(), "https://research.example/start")
		if err != nil || snapshot.FinalURL != "https://mirror.example/final" || string(snapshot.RawContent) != "final content" {
			t.Fatalf("redirect snapshot=%+v err=%v", snapshot, err)
		}
	})
	t.Run("denied redirect", func(t *testing.T) {
		fetcher, err := NewFetcher([]string{"research.example"})
		if err != nil {
			t.Fatal(err)
		}
		fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return responseFor(request, http.StatusFound, "text/plain", "https://internal.example/secret", io.NopCloser(strings.NewReader("redirect"))), nil
		})
		if _, err := fetcher.Fetch(context.Background(), "https://research.example/start"); err == nil {
			t.Fatal("redirect to non-allowlisted host was followed")
		}
	})
	t.Run("redirect limit", func(t *testing.T) {
		fetcher, err := NewFetcher([]string{"research.example"})
		if err != nil {
			t.Fatal(err)
		}
		fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			next := request.URL
			next.Path = "/redirect"
			return responseFor(request, http.StatusFound, "text/plain", next.String(), io.NopCloser(strings.NewReader("redirect"))), nil
		})
		if _, err := fetcher.Fetch(context.Background(), "https://research.example/start"); err == nil {
			t.Fatal("more than five redirects were followed")
		}
	})
}

func TestFetcherRejectsTimeoutMIMEAndOversizedBody(t *testing.T) {
	t.Run("context timeout", func(t *testing.T) {
		fetcher, _ := NewFetcher([]string{"research.example"})
		fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if _, err := fetcher.Fetch(ctx, "https://research.example/slow"); err == nil {
			t.Fatal("timed-out fetch succeeded")
		}
	})
	t.Run("invalid MIME", func(t *testing.T) {
		fetcher, _ := NewFetcher([]string{"research.example"})
		fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return responseFor(request, http.StatusOK, "application/javascript", "", io.NopCloser(strings.NewReader("alert(1)"))), nil
		})
		if _, err := fetcher.Fetch(context.Background(), "https://research.example/script"); err == nil {
			t.Fatal("active MIME type was accepted")
		}
	})
	t.Run("oversized body", func(t *testing.T) {
		fetcher, _ := NewFetcher([]string{"research.example"})
		fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return responseFor(request, http.StatusOK, "text/plain", "", io.NopCloser(strings.NewReader(strings.Repeat("x", MaxBodyBytes+1)))), nil
		})
		if _, err := fetcher.Fetch(context.Background(), "https://research.example/large"); err == nil {
			t.Fatal("oversized response was accepted")
		}
	})
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

func TestFetcherPinnedDialRejectsPrivateDNSResolution(t *testing.T) {
	fetcher, err := NewFetcher([]string{"research.example"})
	if err != nil {
		t.Fatal(err)
	}
	fetcher.resolver = resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil
	})
	if _, err := fetcher.Fetch(context.Background(), "https://research.example/metadata"); err == nil {
		t.Fatal("Fetcher connected when allowlisted hostname resolved to cloud metadata IP")
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
