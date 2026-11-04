package source

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	tagPattern    = regexp.MustCompile(`<[^>]*>`)
	scriptPattern = regexp.MustCompile(`(?is)<(?:script|style|noscript)\b[^>]*>.*?</(?:script|style|noscript)\s*>`)
)

type Fetcher struct {
	allowed  map[string]struct{}
	client   *http.Client
	now      func() time.Time
	resolver *net.Resolver
}

func NewFetcher(allowedHosts []string) (*Fetcher, error) {
	allowed := make(map[string]struct{}, len(allowedHosts))
	for _, host := range allowedHosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || strings.ContainsAny(host, "/:@*\\\r\n\x00") || strings.HasSuffix(host, ".") {
			return nil, fmt.Errorf("invalid source host allowlist entry %q", host)
		}
		allowed[host] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, errors.New("source fetcher requires an exact host allowlist")
	}
	fetcher := &Fetcher{allowed: allowed, now: time.Now, resolver: net.DefaultResolver}
	transport := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 8 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second, IdleConnTimeout: time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			if _, ok := allowed[strings.ToLower(host)]; !ok {
				return nil, errors.New("source target is not allowlisted")
			}
			ips, err := fetcher.resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, errors.New("source target did not resolve")
			}
			for _, item := range ips {
				if !publicIP(item.IP) {
					return nil, errors.New("source target resolves to a non-public address")
				}
			}
			dialer := net.Dialer{Timeout: 5 * time.Second, KeepAlive: -1}
			var lastErr error
			for _, item := range ips {
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(item.IP.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			return nil, fmt.Errorf("dial public source address: %w", lastErr)
		},
	}
	fetcher.client = &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("source redirect limit exceeded")
		}
		return fetcher.validateTarget(request.URL)
	}}
	return fetcher, nil
}

func (f *Fetcher) validateTarget(target *url.URL) error {
	if target == nil || target.User != nil || (target.Scheme != "http" && target.Scheme != "https") {
		return errors.New("only credential-free HTTP(S) sources are allowed")
	}
	if port := target.Port(); port != "" && port != "80" && port != "443" {
		return errors.New("source port is not allowed")
	}
	if target.Scheme == "https" && target.Port() == "80" || target.Scheme == "http" && target.Port() == "443" {
		return errors.New("source scheme and port do not match")
	}
	if _, ok := f.allowed[strings.ToLower(target.Hostname())]; !ok {
		return errors.New("source host is not allowlisted")
	}
	return nil
}

func (f *Fetcher) Fetch(ctx context.Context, originalURL string) (Snapshot, error) {
	if f == nil || f.client == nil {
		return Snapshot{}, errors.New("source fetcher is not configured")
	}
	parsed, err := url.ParseRequestURI(originalURL)
	if err != nil {
		return Snapshot{}, errors.New("source URL is invalid")
	}
	if err := f.validateTarget(parsed); err != nil {
		return Snapshot{}, err
	}
	if len(originalURL) > 2048 {
		return Snapshot{}, errors.New("source URL exceeds limit")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Snapshot{}, err
	}
	request.Header.Set("Accept", "text/plain, text/html, application/json, application/xml, application/xhtml+xml")
	request.Header.Set("User-Agent", "PersonalAIOS-SourceFetcher/1.0 (+local research snapshot)")
	response, err := f.client.Do(request)
	if err != nil {
		return Snapshot{}, fmt.Errorf("fetch source: %w", err)
	}
	defer response.Body.Close()
	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if !allowedMIME(mimeType) {
		return Snapshot{}, fmt.Errorf("source MIME type %q is not allowed", mimeType)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxBodyBytes+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read source body: %w", err)
	}
	if len(body) > MaxBodyBytes {
		return Snapshot{}, errors.New("source response exceeds size limit")
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	normalized := NormalizeContent(mimeType, body)
	finalURL := originalURL
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String()
	}
	return Snapshot{
		OriginalURL: originalURL, FinalURL: finalURL, HTTPStatus: response.StatusCode,
		MIMEType: mimeType, RetrievedAt: f.now().UTC(), ContentHash: Hash(body), RawContent: body,
		NormalizedContent: normalized, FetcherVersion: FetcherVersion,
	}, nil
}

func allowedMIME(value string) bool {
	switch value {
	case "text/plain", "text/html", "application/xhtml+xml", "application/json", "application/xml", "text/xml":
		return true
	default:
		return false
	}
}

func NormalizeContent(mimeType string, content []byte) string {
	value := string(content)
	if mimeType == "text/html" || mimeType == "application/xhtml+xml" {
		value = scriptPattern.ReplaceAllString(value, " ")
		value = tagPattern.ReplaceAllString(value, " ")
		value = html.UnescapeString(value)
	}
	return strings.Join(strings.Fields(value), " ")
}

func publicIP(ip net.IP) bool {
	if mapped := ip.To4(); mapped != nil {
		ip = mapped
	}
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001::/23", "2001:20::/28", "2001:db8::/32"} {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil && network.Contains(ip) {
			return false
		}
	}
	return true
}
