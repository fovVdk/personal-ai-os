// Package builtin registers bounded, read-only native Tools for the Tool Gateway.
// 中文：builtin 包注册受限的只读 Native Tool，供 Tool Gateway 统一调用。
package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"personal-ai-os/internal/knowledge"
	"personal-ai-os/internal/memory"
	"personal-ai-os/internal/tool/registry"
)

const (
	providerID   = "paios.native"
	toolVersion  = "v1"
	maxFileBytes = 512 << 10
	maxHTTPBytes = 1 << 20
)

type Options struct {
	FileRoot              string
	OwnerID               string
	Memories              memory.Repository
	Knowledge             knowledge.Repository
	HTTPFetchAllowedHosts []string
	Now                   func() time.Time
}

func Register(tools *registry.Registry, options Options) error {
	if tools == nil {
		return errors.New("Tool registry is required")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	if err := register(tools, definition("time.now", "Get current UTC time", "{}", nil, "result", registry.KindReadOnly, 0), handlerFunc(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return resultJSON(now().UTC().Format(time.RFC3339Nano))
	})); err != nil {
		return err
	}
	if strings.TrimSpace(options.FileRoot) != "" {
		root, err := newFileRoot(options.FileRoot)
		if err != nil {
			return fmt.Errorf("configure filesystem Tools: %w", err)
		}
		if err := register(tools, definition("filesystem.read", "Read a regular file beneath the configured root", `{"path":{"type":"string"}}`, []string{"path"}, "result", registry.KindReadOnly, 0), readHandler{root}); err != nil {
			return err
		}
		if err := register(tools, definition("filesystem.list", "List one directory beneath the configured root", `{"path":{"type":"string"}}`, []string{"path"}, "result", registry.KindReadOnly, 0), listHandler{root}); err != nil {
			return err
		}
	}
	if options.Memories != nil {
		if options.OwnerID == "" {
			return errors.New("memory Tool requires an owner id")
		}
		if err := register(tools, definition("memory.search", "Search personal memories by keyword", `{"query":{"type":"string"},"limit":{"type":"integer"}}`, []string{"query"}, "result", registry.KindReadOnly, 0), memorySearch{options.Memories, options.OwnerID}); err != nil {
			return err
		}
	}
	if options.Knowledge != nil {
		if options.OwnerID == "" {
			return errors.New("knowledge Tool requires an owner id")
		}
		if err := register(tools, definition("knowledge.search", "Search indexed knowledge and return citations", `{"query":{"type":"string"},"limit":{"type":"integer"}}`, []string{"query"}, "result", registry.KindReadOnly, 0), knowledgeSearch{options.Knowledge, options.OwnerID}); err != nil {
			return err
		}
	}
	if len(options.HTTPFetchAllowedHosts) > 0 {
		hosts := make(map[string]struct{}, len(options.HTTPFetchAllowedHosts))
		for _, host := range options.HTTPFetchAllowedHosts {
			hosts[strings.ToLower(host)] = struct{}{}
		}
		fetch, err := newHTTPFetch(hosts)
		if err != nil {
			return err
		}
		if err := register(tools, definition("http.fetch", "Fetch a public HTTP(S) URL from an exact allowlisted host", `{"url":{"type":"string"}}`, []string{"url"}, "result", registry.KindReadOnly, 1), fetch); err != nil {
			return err
		}
	}
	return nil
}

type handlerFunc func(context.Context, json.RawMessage) (json.RawMessage, error)

func (f handlerFunc) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	return f(ctx, args)
}

type inputSchema struct {
	Type                 string                       `json:"type"`
	Properties           map[string]map[string]string `json:"properties"`
	Required             []string                     `json:"required,omitempty"`
	AdditionalProperties bool                         `json:"additionalProperties"`
}

func definition(id, description, propertiesJSON string, requiredFields []string, resultName string, kind registry.Kind, risk int) registry.Definition {
	properties := map[string]map[string]string{}
	_ = json.Unmarshal([]byte(propertiesJSON), &properties)
	input := inputSchema{Type: "object", Properties: properties, AdditionalProperties: false}
	input.Required = append(input.Required, requiredFields...)
	sort.Strings(input.Required)
	encoded, _ := json.Marshal(input)
	output := inputSchema{Type: "object", Properties: map[string]map[string]string{resultName: {"type": "string"}}, Required: []string{resultName}, AdditionalProperties: false}
	outputBytes, _ := json.Marshal(output)
	return registry.Definition{Identity: registry.Identity{ProviderID: providerID, ToolID: id, Version: toolVersion}, Name: id, Description: description, Source: registry.SourceBuiltin, Kind: kind, RiskLevel: risk, Idempotency: registry.IdempotencyIntrinsic, Timeout: 15 * time.Second, MaxInputBytes: 16 << 10, MaxOutputBytes: 2 << 20, ExecutionEnvironment: "local", InputSchema: encoded, OutputSchema: outputBytes}
}

func register(tools *registry.Registry, definition registry.Definition, handler registry.Handler) error {
	if err := tools.Register(definition, handler); err != nil {
		return fmt.Errorf("register Tool %s: %w", definition.Identity.ToolID, err)
	}
	return nil
}

func resultJSON(value string) (json.RawMessage, error) {
	encoded, err := json.Marshal(struct {
		Result string `json:"result"`
	}{Result: value})
	return encoded, err
}

type fileRoot struct{ root string }

func newFileRoot(root string) (fileRoot, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fileRoot{}, fmt.Errorf("resolve configured file root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return fileRoot{}, errors.New("configured file root must be an existing directory")
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return fileRoot{}, err
	}
	return fileRoot{root: absolute}, nil
}

func (r fileRoot) resolve(relative string) (string, error) {
	if relative == "" {
		relative = "."
	}
	if filepath.IsAbs(relative) || strings.ContainsAny(relative, "\\\x00") {
		return "", errors.New("path must be relative to the configured root")
	}
	clean := filepath.Clean(relative)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the configured root")
	}
	candidate := filepath.Join(r.root, clean)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve requested path: %w", err)
	}
	rel, err := filepath.Rel(r.root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("resolved path escapes the configured root")
	}
	return resolved, nil
}

type readHandler struct{ root fileRoot }

func (h readHandler) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, err
	}
	path, err := h.root.resolve(input.Path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("filesystem.read accepts regular files only")
	}
	if info.Size() > maxFileBytes {
		return nil, errors.New("file exceeds read limit")
	}
	limited := io.LimitReader(file, maxFileBytes+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(content) > maxFileBytes {
		return nil, errors.New("file exceeds read limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return resultJSON(string(content))
}

type listHandler struct{ root fileRoot }

func (h listHandler) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, err
	}
	path, err := h.root.resolve(input.Path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	if len(entries) > 1000 {
		return nil, errors.New("directory contains too many entries")
	}
	var output strings.Builder
	for index, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if index > 0 {
			output.WriteByte('\n')
		}
		output.WriteString(entry.Name())
		if entry.IsDir() {
			output.WriteByte('/')
		}
	}
	return resultJSON(output.String())
}

type memorySearch struct {
	repo    memory.Repository
	ownerID string
}

func (h memorySearch) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, err
	}
	if input.Limit < 1 || input.Limit > 50 {
		input.Limit = 10
	}
	items, err := h.repo.Search(ctx, h.ownerID, input.Query, input.Limit)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return resultJSON(string(encoded))
}

type knowledgeSearch struct {
	repo    knowledge.Repository
	ownerID string
}

func (h knowledgeSearch) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, err
	}
	if input.Limit < 1 || input.Limit > 50 {
		input.Limit = 10
	}
	items, err := h.repo.Search(ctx, h.ownerID, input.Query, input.Limit)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return resultJSON(string(encoded))
}

func newHTTPFetch(allowed map[string]struct{}) (registry.Handler, error) {
	if len(allowed) == 0 {
		return nil, errors.New("http.fetch requires an exact host allowlist")
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 8 * time.Second, TLSHandshakeTimeout: 5 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if _, ok := allowed[strings.ToLower(host)]; !ok {
			return nil, errors.New("HTTP target host is not allowlisted")
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("HTTP target did not resolve")
		}
		dialer := net.Dialer{Timeout: 5 * time.Second, KeepAlive: -1}
		for _, item := range ips {
			if !publicAddress(item.IP) {
				continue
			}
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(item.IP.String(), port))
			if dialErr == nil {
				return connection, nil
			}
		}
		return nil, errors.New("HTTP target resolves only to non-public or unreachable addresses")
	}}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return handlerFunc(func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
		var input struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(args, &input); err != nil {
			return nil, err
		}
		if len(input.URL) > 2048 {
			return nil, errors.New("URL exceeds limit")
		}
		parsed, err := url.ParseRequestURI(input.URL)
		if err != nil || parsed.Hostname() == "" || parsed.User != nil {
			return nil, errors.New("URL must be absolute HTTP(S) without user credentials")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, errors.New("only HTTP and HTTPS URLs are allowed")
		}
		port := parsed.Port()
		if port != "" && port != "80" && port != "443" {
			return nil, errors.New("HTTP target port is not allowed")
		}
		if parsed.Scheme == "https" && port == "80" || parsed.Scheme == "http" && port == "443" {
			return nil, errors.New("URL scheme and port do not match")
		}
		if _, ok := allowed[strings.ToLower(parsed.Hostname())]; !ok {
			return nil, errors.New("HTTP target host is not allowlisted")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "text/plain, text/html, application/json")
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			return nil, errors.New("HTTP redirects are not followed")
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, maxHTTPBytes+1))
		if err != nil {
			return nil, err
		}
		if len(body) > maxHTTPBytes {
			return nil, errors.New("HTTP response exceeds size limit")
		}
		return resultJSON(string(body))
	}), nil
}

func publicAddress(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	blocked := []*net.IPNet{}
	for _, cidr := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "2001:db8::/32"} {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil {
			blocked = append(blocked, network)
		}
	}
	for _, network := range blocked {
		if network.Contains(ip) {
			return false
		}
	}
	return true
}
