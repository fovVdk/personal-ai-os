// Package mcp adapts the official MCP SDK to the Go Core Tool Registry.
// 中文：mcp 包将官方 MCP SDK 适配到 Go Core Tool Registry。
package mcp

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"personal-ai-os/internal/tool/registry"
)

// TransportKind selects one explicitly configured MCP transport.
// 中文：TransportKind 表示运维显式配置的 MCP 传输方式。
type TransportKind string

const (
	TransportStreamableHTTP TransportKind = "streamable_http"
	TransportStdio          TransportKind = "stdio"
)

// ServerConfig is operator-owned and is never populated from Agent or model input.
// 中文：ServerConfig 只由运维配置，不能由 Agent 或模型输入生成。
type ServerConfig struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Transport    TransportKind     `json:"transport"`
	Endpoint     string            `json:"endpoint,omitempty"`
	AllowedHosts []string          `json:"allowed_hosts,omitempty"`
	Command      string            `json:"command,omitempty"`
	Args         []string          `json:"args,omitempty"`
	WorkingDir   string            `json:"working_dir,omitempty"`
	Environment  map[string]string `json:"environment,omitempty"`
	AllowedTools []string          `json:"allowed_tools,omitempty"`
	Enabled      bool              `json:"enabled"`
}

// Options bounds all network, discovery, and tool-call work.
// 中文：Options 为网络连接、Tool 发现和调用设定资源上限。
type Options struct {
	ConnectTimeout time.Duration
	CallTimeout    time.Duration
	HealthInterval time.Duration
	ReconnectMin   time.Duration
	ReconnectMax   time.Duration
	MaxTools       int
	MaxPages       int
	MaxSchemaBytes int
	MaxOutputBytes int
}

type ServerStatus struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	Transport        TransportKind `json:"transport"`
	Enabled          bool          `json:"enabled"`
	ConnectionStatus string        `json:"connection_status"`
	ServerVersion    string        `json:"server_version,omitempty"`
	LastConnectedAt  *time.Time    `json:"last_connected_at,omitempty"`
	LastError        string        `json:"last_error,omitempty"`
	ToolDiscoveryAt  *time.Time    `json:"tool_discovery_at,omitempty"`
	ToolCount        int           `json:"tool_count"`
}

type managedServer struct {
	config     ServerConfig
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.RWMutex
	connectMu  sync.Mutex
	refreshMu  sync.Mutex
	session    *sdk.ClientSession
	status     ServerStatus
	identities map[registry.Identity]string
	wake       chan struct{}
}

type Manager struct {
	registry *registry.Registry
	options  Options
	mu       sync.RWMutex
	servers  map[string]*managedServer
	ctx      context.Context
	cancel   context.CancelFunc
	started  bool
	closed   bool
	workers  sync.WaitGroup
}

var serverIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,59}$`)
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// NewManager constructs a bounded MCP manager around the shared Tool Registry.
// 中文：NewManager 在共享 Tool Registry 上创建受资源限制的 MCP Manager。
func NewManager(tools *registry.Registry, options Options) (*Manager, error) {
	if tools == nil {
		return nil, errors.New("MCP manager requires a Tool Registry")
	}
	if options.ConnectTimeout == 0 {
		options.ConnectTimeout = 10 * time.Second
	}
	if options.CallTimeout == 0 {
		options.CallTimeout = 30 * time.Second
	}
	if options.HealthInterval == 0 {
		options.HealthInterval = 15 * time.Second
	}
	if options.ReconnectMin == 0 {
		options.ReconnectMin = 500 * time.Millisecond
	}
	if options.ReconnectMax == 0 {
		options.ReconnectMax = 15 * time.Second
	}
	if options.MaxTools == 0 {
		options.MaxTools = 128
	}
	if options.MaxPages == 0 {
		options.MaxPages = 8
	}
	if options.MaxSchemaBytes == 0 {
		options.MaxSchemaBytes = 256 << 10
	}
	if options.MaxOutputBytes == 0 {
		options.MaxOutputBytes = 1 << 20
	}
	if options.ConnectTimeout < time.Second || options.ConnectTimeout > time.Minute || options.CallTimeout < time.Millisecond || options.CallTimeout > 10*time.Minute || options.HealthInterval < time.Second || options.HealthInterval > 5*time.Minute || options.ReconnectMin < 10*time.Millisecond || options.ReconnectMax < options.ReconnectMin || options.MaxTools < 1 || options.MaxTools > 1024 || options.MaxPages < 1 || options.MaxPages > 64 || options.MaxSchemaBytes < 1024 || options.MaxSchemaBytes > 1<<20 || options.MaxOutputBytes < 1024 || options.MaxOutputBytes > 4<<20 {
		return nil, errors.New("MCP manager limits are invalid")
	}
	return &Manager{registry: tools, options: options, servers: make(map[string]*managedServer)}, nil
}

// Start registers operator-defined servers and starts bounded connect/reconnect workers.
// 中文：Start 注册运维配置的 Server，并启动有界连接/重连 Worker。
func (m *Manager) Start(ctx context.Context, configs []ServerConfig) error {
	if m == nil || ctx == nil {
		return errors.New("MCP manager and context are required")
	}
	m.mu.Lock()
	if m.closed || m.started {
		m.mu.Unlock()
		return errors.New("MCP manager is already started or closed")
	}
	seen := make(map[string]bool, len(configs))
	for _, config := range configs {
		if err := validateServerConfig(config); err != nil {
			m.mu.Unlock()
			return fmt.Errorf("invalid MCP server configuration: %w", err)
		}
		if seen[config.ID] {
			m.mu.Unlock()
			return fmt.Errorf("duplicate MCP server id %q", config.ID)
		}
		seen[config.ID] = true
		if _, exists := m.servers[config.ID]; exists {
			m.mu.Unlock()
			return fmt.Errorf("duplicate MCP server id %q", config.ID)
		}
	}
	for _, config := range configs {
		copyConfig := cloneServerConfig(config)
		m.servers[config.ID] = &managedServer{config: copyConfig, status: ServerStatus{ID: config.ID, Name: config.Name, Transport: config.Transport, Enabled: config.Enabled, ConnectionStatus: "disabled"}, identities: make(map[registry.Identity]string), wake: make(chan struct{}, 1)}
	}
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.started = true
	servers := make([]*managedServer, 0, len(m.servers))
	contexts := make([]context.Context, 0, len(m.servers))
	for _, server := range m.servers {
		server.ctx, server.cancel = context.WithCancel(m.ctx)
		m.workers.Add(1)
		servers = append(servers, server)
		contexts = append(contexts, server.ctx)
	}
	m.mu.Unlock()
	for index, server := range servers {
		go m.runServer(server, contexts[index])
	}
	return nil
}

func (m *Manager) runServer(server *managedServer, serverCtx context.Context) {
	defer m.workers.Done()
	backoff := m.options.ReconnectMin
	for {
		if serverCtx.Err() != nil {
			return
		}
		server.mu.RLock()
		enabled := server.config.Enabled
		server.mu.RUnlock()
		if !enabled {
			select {
			case <-serverCtx.Done():
				return
			case <-server.wake:
				continue
			}
		}
		server.mu.Lock()
		if server.config.Enabled {
			server.status.ConnectionStatus = "connecting"
		}
		server.mu.Unlock()
		session, err := m.connectAndDiscover(serverCtx, server)
		if err != nil {
			server.mu.Lock()
			server.status.ConnectionStatus = "disconnected"
			server.status.LastError = "MCP connection or tool discovery failed"
			server.mu.Unlock()
			if !waitContext(serverCtx, server.wake, backoff) {
				return
			}
			backoff = minDuration(backoff*2, m.options.ReconnectMax)
			continue
		}
		backoff = m.options.ReconnectMin
		waitErr := m.waitForSession(serverCtx, server, session)
		if serverCtx.Err() != nil {
			_ = session.Close()
			return
		}
		server.mu.Lock()
		if server.session == session {
			server.session = nil
			server.status.ConnectionStatus = "disconnected"
			if server.status.Enabled {
				server.status.LastError = "MCP server connection closed"
			}
		}
		enabled = server.config.Enabled
		server.mu.Unlock()
		m.setToolHealth(server, false)
		_ = session.Close()
		_ = waitErr // transport errors are intentionally not logged; they may contain endpoint credentials.
		if !enabled {
			continue
		}
		if !waitContext(serverCtx, server.wake, backoff) {
			return
		}
		backoff = minDuration(backoff*2, m.options.ReconnectMax)
	}
}

func (m *Manager) waitForSession(ctx context.Context, server *managedServer, session *sdk.ClientSession) error {
	waitResult := make(chan error, 1)
	go func() { waitResult <- session.Wait() }()
	ticker := time.NewTicker(m.options.HealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = session.Close()
			return ctx.Err()
		case err := <-waitResult:
			return err
		case <-ticker.C:
			healthCtx, cancel := context.WithTimeout(ctx, m.options.ConnectTimeout)
			err := m.RefreshTools(healthCtx, server.config.ID)
			cancel()
			if err != nil {
				server.mu.Lock()
				server.status.LastError = "MCP health check or tool discovery failed"
				server.mu.Unlock()
				_ = session.Close()
				return err
			}
		}
	}
}

func (m *Manager) connectAndDiscover(serverCtx context.Context, server *managedServer) (*sdk.ClientSession, error) {
	server.connectMu.Lock()
	defer server.connectMu.Unlock()
	server.mu.RLock()
	enabled := server.config.Enabled
	server.mu.RUnlock()
	if !enabled {
		return nil, errors.New("MCP server is disabled")
	}
	m.setToolHealth(server, false)
	ctx, cancel := context.WithTimeout(serverCtx, m.options.ConnectTimeout)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "personal-ai-os", Version: "1.0.0"}, &sdk.ClientOptions{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Capabilities: &sdk.ClientCapabilities{},
		ToolListChangedHandler: func(context.Context, *sdk.ToolListChangedRequest) {
			go func() {
				refreshCtx, stop := context.WithTimeout(serverCtx, m.options.ConnectTimeout)
				defer stop()
				_ = m.RefreshTools(refreshCtx, server.config.ID)
			}()
		},
	})
	transport, err := m.makeTransport(server.config)
	if err != nil {
		return nil, err
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}
	server.mu.Lock()
	server.session = session
	server.status.ConnectionStatus = "discovering"
	server.status.ServerVersion = ""
	if result := session.InitializeResult(); result != nil && result.ServerInfo != nil {
		server.status.ServerVersion = result.ServerInfo.Version
	}
	server.mu.Unlock()
	server.refreshMu.Lock()
	if err := m.discoverIntoRegistry(ctx, server, session); err != nil {
		server.refreshMu.Unlock()
		server.mu.Lock()
		if server.session == session {
			server.session = nil
		}
		server.status.ConnectionStatus = "disconnected"
		server.mu.Unlock()
		m.setToolHealth(server, false)
		_ = session.Close()
		return nil, err
	}
	server.refreshMu.Unlock()
	now := time.Now().UTC()
	server.mu.Lock()
	if server.session == session {
		server.status.ConnectionStatus = "connected"
		server.status.LastConnectedAt = &now
		server.status.LastError = ""
	}
	server.mu.Unlock()
	return session, nil
}

func (m *Manager) makeTransport(config ServerConfig) (sdk.Transport, error) {
	switch config.Transport {
	case TransportStreamableHTTP:
		client, err := safeHTTPClient(config)
		if err != nil {
			return nil, err
		}
		return &sdk.StreamableClientTransport{Endpoint: config.Endpoint, HTTPClient: client, MaxRetries: 2, MaxEventSize: m.options.MaxOutputBytes, DisableStandaloneSSE: false}, nil
	case TransportStdio:
		command := exec.Command(config.Command, config.Args...)
		command.Dir = config.WorkingDir
		command.Env = make([]string, 0, len(config.Environment))
		for key, value := range config.Environment {
			command.Env = append(command.Env, key+"="+value)
		}
		sort.Strings(command.Env)
		command.Stderr = io.Discard // MCP stdio protocol is stdout-only; stderr is untrusted and may contain secrets.
		return &sdk.CommandTransport{Command: command, TerminateDuration: 2 * time.Second}, nil
	default:
		return nil, errors.New("unsupported MCP transport")
	}
}

// RegisterServer adds a disabled server definition before Start; it launches no process or network request.
// 中文：RegisterServer 在 Start 前添加禁用的 Server 定义，不启动进程或网络请求。
func (m *Manager) RegisterServer(config ServerConfig) error {
	if err := validateServerConfig(config); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.started {
		return errors.New("register MCP servers before Start")
	}
	if _, exists := m.servers[config.ID]; exists {
		return fmt.Errorf("duplicate MCP server id %q", config.ID)
	}
	config.Enabled = false
	m.servers[config.ID] = &managedServer{config: cloneServerConfig(config), status: ServerStatus{ID: config.ID, Name: config.Name, Transport: config.Transport, ConnectionStatus: "disabled"}, identities: make(map[registry.Identity]string), wake: make(chan struct{}, 1)}
	return nil
}

// SetEnabled changes only operator-owned lifecycle state.
// 中文：SetEnabled 只修改运维配置的 Server 生命周期状态。
func (m *Manager) SetEnabled(id string, enabled bool) error {
	server, err := m.getServer(id)
	if err != nil {
		return err
	}
	server.mu.Lock()
	server.config.Enabled = enabled
	server.status.Enabled = enabled
	if !enabled {
		server.status.ConnectionStatus = "disabled"
		server.status.LastError = ""
	}
	session := server.session
	if !enabled {
		server.session = nil
	}
	serverCancel := server.cancel
	server.mu.Unlock()
	m.setToolHealth(server, false)
	if !enabled && serverCancel != nil {
		serverCancel()
	}
	if session != nil && !enabled {
		_ = session.Close()
	}
	if enabled {
		signal(server.wake)
		m.startServerWorker(server)
	}
	return nil
}

func (m *Manager) Reconnect(id string) error {
	server, err := m.getServer(id)
	if err != nil {
		return err
	}
	server.mu.Lock()
	if !server.config.Enabled {
		server.mu.Unlock()
		return errors.New("MCP server is disabled")
	}
	session := server.session
	server.session = nil
	server.status.ConnectionStatus = "disconnected"
	wasRunning := server.ctx != nil && server.ctx.Err() == nil
	server.mu.Unlock()
	m.setToolHealth(server, false)
	if session != nil {
		_ = session.Close()
	}
	signal(server.wake)
	if !wasRunning {
		m.startServerWorker(server)
	}
	return nil
}

func (m *Manager) RemoveServer(id string) error {
	server, err := m.getServer(id)
	if err != nil {
		return err
	}
	server.mu.Lock()
	server.config.Enabled = false
	session := server.session
	server.session = nil
	cancel := server.cancel
	server.status.Enabled = false
	server.status.ConnectionStatus = "disabled"
	server.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.setToolHealth(server, false)
	if session != nil {
		_ = session.Close()
	}
	m.mu.Lock()
	delete(m.servers, id)
	m.mu.Unlock()
	signal(server.wake)
	return nil
}

func (m *Manager) startServerWorker(server *managedServer) {
	m.mu.Lock()
	if !m.started || m.closed {
		m.mu.Unlock()
		return
	}
	if registered, ok := m.servers[server.config.ID]; !ok || registered != server {
		m.mu.Unlock()
		return
	}
	server.mu.Lock()
	if server.ctx != nil && server.ctx.Err() == nil {
		server.mu.Unlock()
		m.mu.Unlock()
		return
	}
	server.ctx, server.cancel = context.WithCancel(m.ctx)
	serverCtx := server.ctx
	server.mu.Unlock()
	m.workers.Add(1)
	m.mu.Unlock()
	go m.runServer(server, serverCtx)
}

// RefreshTools rediscovers and validates the tool list for a connected Server.
// 中文：RefreshTools 重新发现并校验已连接 Server 的工具清单。
func (m *Manager) RefreshTools(ctx context.Context, id string) error {
	server, err := m.getServer(id)
	if err != nil {
		return err
	}
	server.refreshMu.Lock()
	defer server.refreshMu.Unlock()
	server.mu.RLock()
	session := server.session
	connected := server.status.ConnectionStatus == "connected"
	server.mu.RUnlock()
	if session == nil || !connected {
		return errors.New("MCP server is not connected")
	}
	m.setToolHealth(server, false)
	if err := m.discoverIntoRegistry(ctx, server, session); err != nil {
		return err
	}
	return nil
}

func (m *Manager) discoverIntoRegistry(ctx context.Context, server *managedServer, session *sdk.ClientSession) error {
	tools := make([]*sdk.Tool, 0)
	cursor := ""
	for page := 0; page < m.options.MaxPages; page++ {
		result, err := session.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return fmt.Errorf("MCP tools/list failed: %w", err)
		}
		tools = append(tools, result.Tools...)
		if len(tools) > m.options.MaxTools {
			return errors.New("MCP server returned too many tools")
		}
		if result.NextCursor == "" {
			cursor = ""
			break
		}
		if result.NextCursor == cursor {
			return errors.New("MCP server repeated a tool pagination cursor")
		}
		cursor = result.NextCursor
		if page == m.options.MaxPages-1 {
			return errors.New("MCP server exceeded the tool pagination limit")
		}
	}
	seen := make(map[registry.Identity]string, len(tools))
	for _, tool := range tools {
		definition, err := m.definitionFor(server, session, tool)
		if err != nil {
			return fmt.Errorf("reject MCP tool definition: %w", err)
		}
		var remoteOutputSchema json.RawMessage
		if tool.OutputSchema != nil {
			outputRaw, marshalErr := json.Marshal(tool.OutputSchema)
			if marshalErr != nil {
				return fmt.Errorf("encode MCP output schema: %w", marshalErr)
			}
			remoteOutputSchema, err = sanitizeObjectSchema(outputRaw, m.options.MaxSchemaBytes)
			if err != nil {
				return fmt.Errorf("reject MCP output schema: %w", err)
			}
		}
		handler := mcpToolHandler{server: server, identity: definition.Identity, name: tool.Name, timeout: m.options.CallTimeout, maxOutput: m.options.MaxOutputBytes, outputSchema: remoteOutputSchema}
		if err := m.registry.Register(definition, handler); err != nil && !errors.Is(err, registry.ErrExists) {
			return fmt.Errorf("register MCP tool: %w", err)
		} else if errors.Is(err, registry.ErrExists) {
			if replaceErr := m.registry.ReplaceHandler(definition.Identity, handler); replaceErr != nil {
				return fmt.Errorf("replace MCP Tool runtime handler: %w", replaceErr)
			}
		}
		seen[definition.Identity] = tool.Name
	}
	server.mu.Lock()
	previous := server.identities
	server.identities = seen
	server.status.ToolCount = len(seen)
	now := time.Now().UTC()
	server.status.ToolDiscoveryAt = &now
	server.mu.Unlock()
	for identity := range previous {
		if _, ok := seen[identity]; !ok {
			_ = m.registry.SetOperationalState(identity, false, registry.HealthUnhealthy)
		}
	}
	for identity := range seen {
		if err := m.registry.SetOperationalState(identity, true, registry.HealthHealthy); err != nil {
			return fmt.Errorf("enable discovered MCP Tool: %w", err)
		}
	}
	return nil
}

func (m *Manager) definitionFor(server *managedServer, session *sdk.ClientSession, tool *sdk.Tool) (registry.Definition, error) {
	if tool == nil || !serverToolNamePattern.MatchString(tool.Name) || len(tool.Name) > 128 || len(tool.Description) > 4096 || strings.ContainsAny(tool.Description, "\x00\r") {
		return registry.Definition{}, errors.New("MCP tool name or description is invalid")
	}
	inputRaw, err := json.Marshal(tool.InputSchema)
	if err != nil || len(inputRaw) > m.options.MaxSchemaBytes {
		return registry.Definition{}, errors.New("MCP input schema is invalid or too large")
	}
	inputSchema, err := sanitizeObjectSchema(inputRaw, m.options.MaxSchemaBytes)
	if err != nil {
		return registry.Definition{}, err
	}
	var outputSchema json.RawMessage
	if tool.OutputSchema != nil {
		outputRaw, err := json.Marshal(tool.OutputSchema)
		if err != nil || len(outputRaw) > m.options.MaxSchemaBytes {
			return registry.Definition{}, errors.New("MCP output schema is invalid or too large")
		}
		outputSchema, err = sanitizeObjectSchema(outputRaw, m.options.MaxSchemaBytes)
		if err != nil {
			return registry.Definition{}, fmt.Errorf("MCP output schema: %w", err)
		}
	}
	serverVersion := "unknown"
	if result := session.InitializeResult(); result != nil && result.ServerInfo != nil {
		serverVersion = result.ServerInfo.Version
	}
	fingerprintData, _ := json.Marshal(struct {
		ServerVersion, Description string
		InputSchema, OutputSchema  json.RawMessage
		Transport                  TransportKind
		Timeout                    time.Duration
		MaxOutput                  int
	}{serverVersion, tool.Description, inputSchema, outputSchema, server.config.Transport, m.options.CallTimeout, m.options.MaxOutputBytes})
	hash := sha256.Sum256(fingerprintData)
	version := "v" + hex.EncodeToString(hash[:6])
	safeName := tool.Name
	if len(safeName) > 48 {
		safeName = safeName[:48]
	}
	nameHash := sha256.Sum256([]byte(tool.Name))
	identity := registry.Identity{ProviderID: "mcp." + server.config.ID, ToolID: "mcp." + server.config.ID + "." + safeName + "." + hex.EncodeToString(nameHash[:4]), Version: version}
	return registry.Definition{Identity: identity, Name: tool.Name, Description: tool.Description, Source: registry.SourceMCP, Kind: registry.KindExternal, RiskLevel: 3, Idempotency: registry.IdempotencyNone, Timeout: m.options.CallTimeout, MaxInputBytes: 1 << 20, MaxOutputBytes: m.options.MaxOutputBytes, ExecutionEnvironment: "mcp-remote", InputSchema: inputSchema, OutputSchema: json.RawMessage(`{"type":"object","properties":{"result":{"type":"string"}},"required":["result"],"additionalProperties":false}`)}, nil
}

var serverToolNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func sanitizeObjectSchema(raw []byte, maxBytes int) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > maxBytes {
		return nil, errors.New("MCP input schema is missing or oversized")
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(raw, &schema); err != nil || schema == nil {
		return nil, errors.New("MCP input schema must be a JSON object")
	}
	for key := range schema {
		switch key {
		case "type", "properties", "required", "additionalProperties", "$schema", "title", "description":
		default:
			return nil, fmt.Errorf("MCP schema keyword %q is unsupported", key)
		}
	}
	var rootType string
	if err := json.Unmarshal(schema["type"], &rootType); err != nil || rootType != "object" {
		return nil, errors.New("MCP input schema root must be object")
	}
	properties := map[string]json.RawMessage{}
	if rawProps := schema["properties"]; len(rawProps) > 0 {
		if err := json.Unmarshal(rawProps, &properties); err != nil {
			return nil, errors.New("MCP schema properties must be an object")
		}
	}
	if len(properties) > 256 {
		return nil, errors.New("MCP schema has too many properties")
	}
	for key, rawProperty := range properties {
		if key == "" || len(key) > 128 {
			return nil, errors.New("MCP schema property name is invalid")
		}
		var property map[string]json.RawMessage
		if err := json.Unmarshal(rawProperty, &property); err != nil || property == nil {
			return nil, errors.New("MCP property schema is invalid")
		}
		for field := range property {
			if field != "type" && field != "title" && field != "description" {
				return nil, fmt.Errorf("MCP property constraint %q is unsupported", field)
			}
		}
		var kind string
		if err := json.Unmarshal(property["type"], &kind); err != nil {
			return nil, errors.New("MCP property type is required")
		}
		switch kind {
		case "string", "boolean", "integer", "number":
		default:
			return nil, errors.New("MCP property type is unsupported")
		}
		properties[key], _ = json.Marshal(map[string]string{"type": kind})
	}
	var required []string
	if len(schema["required"]) > 0 {
		if err := json.Unmarshal(schema["required"], &required); err != nil {
			return nil, errors.New("MCP required must be a string array")
		}
	}
	if len(required) > 256 {
		return nil, errors.New("MCP schema requires too many properties")
	}
	seen := map[string]bool{}
	for _, name := range required {
		if _, ok := properties[name]; !ok || seen[name] {
			return nil, errors.New("MCP required fields must be unique declared properties")
		}
		seen[name] = true
	}
	if extra, ok := schema["additionalProperties"]; ok {
		var allowed bool
		if json.Unmarshal(extra, &allowed) != nil || allowed {
			return nil, errors.New("MCP additionalProperties must be false")
		}
	}
	out := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if required != nil {
		out["required"] = required
	}
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) > maxBytes {
		return nil, errors.New("normalized MCP schema exceeds limit")
	}
	if err := registry.Validate(json.RawMessage(encoded), json.RawMessage(`{}`)); err != nil && !errors.Is(err, registry.ErrInvalidInput) {
		return nil, err
	}
	return encoded, nil
}

type mcpToolHandler struct {
	server       *managedServer
	identity     registry.Identity
	name         string
	timeout      time.Duration
	maxOutput    int
	outputSchema json.RawMessage
}

func (h mcpToolHandler) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	h.server.refreshMu.Lock()
	defer h.server.refreshMu.Unlock()
	if len(args) > 1<<20 {
		return nil, errors.New("MCP arguments exceed limit")
	}
	var arguments map[string]any
	if err := json.Unmarshal(args, &arguments); err != nil || arguments == nil {
		return nil, errors.New("MCP arguments must be a JSON object")
	}
	h.server.mu.RLock()
	session := h.server.session
	connected := h.server.status.ConnectionStatus == "connected" && h.server.config.Enabled
	registeredName, identityRegistered := h.server.identities[h.identity]
	h.server.mu.RUnlock()
	if session == nil || !connected || !identityRegistered || registeredName != h.name {
		return nil, errors.New("MCP server is disconnected or disabled")
	}
	callCtx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	result, err := session.CallTool(callCtx, &sdk.CallToolParams{Name: h.name, Arguments: arguments})
	if err != nil {
		return nil, fmt.Errorf("MCP tool call failed")
	}
	if result == nil {
		return nil, errors.New("MCP server returned an empty tool result")
	}
	if result.IsError {
		return nil, errors.New("MCP server reported a tool error")
	}
	var text strings.Builder
	if result.StructuredContent != nil {
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return nil, errors.New("MCP structured result is invalid")
		}
		if len(h.outputSchema) > 0 {
			if err := registry.Validate(h.outputSchema, encoded); err != nil {
				return nil, errors.New("MCP structured result does not match advertised output schema")
			}
		}
		text.Write(encoded)
	} else {
		if len(h.outputSchema) > 0 {
			return nil, errors.New("MCP server omitted structured content required by its output schema")
		}
		for _, content := range result.Content {
			if item, ok := content.(*sdk.TextContent); ok {
				if text.Len() > 0 {
					text.WriteByte('\n')
				}
				text.WriteString(item.Text)
			} else {
				encoded, err := json.Marshal(content)
				if err != nil {
					return nil, errors.New("MCP content block is invalid")
				}
				if text.Len() > 0 {
					text.WriteByte('\n')
				}
				text.Write(encoded)
			}
		}
	}
	if text.Len() > h.maxOutput {
		return nil, errors.New("MCP result exceeds output limit")
	}
	output, err := json.Marshal(struct {
		Result string `json:"result"`
	}{Result: text.String()})
	if err != nil {
		return nil, err
	}
	return output, nil
}

// IsToolAllowed reports whether the operator allowlist contains this exact discovered Tool identity.
// 中文：IsToolAllowed 检查运维 allowlist 是否包含该精确发现工具身份。
func (m *Manager) IsToolAllowed(identity registry.Identity) bool {
	serverID := strings.TrimPrefix(identity.ProviderID, "mcp.")
	server, err := m.getServer(serverID)
	if err != nil {
		return false
	}
	server.mu.RLock()
	defer server.mu.RUnlock()
	toolName, ok := server.identities[identity]
	if !ok {
		return false
	}
	for _, allowed := range server.config.AllowedTools {
		if allowed == toolName {
			return true
		}
	}
	return false
}

// ValidateServerConfig applies the same strict security checks used by manager registration.
// 中文：ValidateServerConfig 执行与 Manager 注册时相同的严格安全校验。
func ValidateServerConfig(config ServerConfig) error { return validateServerConfig(config) }

// Status returns a deterministic, credential-free snapshot of configured MCP Servers.
// 中文：Status 返回排序稳定且不含凭据的 MCP Server 状态快照。
func (m *Manager) Status() []ServerStatus {
	m.mu.RLock()
	servers := make([]*managedServer, 0, len(m.servers))
	for _, server := range m.servers {
		servers = append(servers, server)
	}
	m.mu.RUnlock()
	result := make([]ServerStatus, 0, len(servers))
	for _, server := range servers {
		server.mu.RLock()
		item := server.status
		server.mu.RUnlock()
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// Close disables connected Servers, closes SDK sessions, and waits for lifecycle workers.
// 中文：Close 禁用已连接 Server、关闭 SDK Session 并等待生命周期 Worker 退出。
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	cancel := m.cancel
	servers := make([]*managedServer, 0, len(m.servers))
	for _, server := range m.servers {
		servers = append(servers, server)
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, server := range servers {
		server.mu.Lock()
		session := server.session
		server.session = nil
		server.status.ConnectionStatus = "disabled"
		server.status.Enabled = false
		server.config.Enabled = false
		server.mu.Unlock()
		m.setToolHealth(server, false)
		if session != nil {
			_ = session.Close()
		}
	}
	m.workers.Wait()
	return nil
}

func (m *Manager) getServer(id string) (*managedServer, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	server, ok := m.servers[id]
	if !ok {
		return nil, fmt.Errorf("MCP server %q is not registered", id)
	}
	return server, nil
}
func (m *Manager) setToolHealth(server *managedServer, enabled bool) {
	server.mu.RLock()
	identities := make([]registry.Identity, 0, len(server.identities))
	for identity := range server.identities {
		identities = append(identities, identity)
	}
	server.mu.RUnlock()
	for _, identity := range identities {
		health := registry.HealthUnhealthy
		if enabled {
			health = registry.HealthHealthy
		}
		_ = m.registry.SetOperationalState(identity, enabled, health)
	}
}
func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
func waitContext(ctx context.Context, wake <-chan struct{}, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-wake:
		return true
	case <-timer.C:
		return true
	}
}
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
func cloneServerConfig(config ServerConfig) ServerConfig {
	config.AllowedHosts = append([]string(nil), config.AllowedHosts...)
	config.Args = append([]string(nil), config.Args...)
	config.AllowedTools = append([]string(nil), config.AllowedTools...)
	if config.Environment != nil {
		env := make(map[string]string, len(config.Environment))
		for k, v := range config.Environment {
			env[k] = v
		}
		config.Environment = env
	}
	return config
}

func validateServerConfig(config ServerConfig) error {
	if !serverIDPattern.MatchString(config.ID) || strings.TrimSpace(config.Name) == "" || len(config.Name) > 256 || strings.ContainsAny(config.Name, "\x00\r\n") {
		return errors.New("MCP server id and name are invalid")
	}
	if len(config.AllowedTools) > 256 {
		return errors.New("MCP allowed tool list exceeds limit")
	}
	seenTools := map[string]bool{}
	for _, name := range config.AllowedTools {
		if !serverToolNamePattern.MatchString(name) || seenTools[name] {
			return errors.New("MCP allowed tool names must be valid and unique")
		}
		seenTools[name] = true
	}
	switch config.Transport {
	case TransportStreamableHTTP:
		parsed, err := url.ParseRequestURI(config.Endpoint)
		if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return errors.New("MCP HTTP endpoint must be absolute HTTP(S) without user credentials or fragment")
		}
		if parsed.Scheme == "http" {
			ip := net.ParseIP(parsed.Hostname())
			if !strings.EqualFold(parsed.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback()) {
				return errors.New("non-loopback MCP HTTP endpoints must use HTTPS")
			}
		}
		if len(config.AllowedHosts) == 0 {
			return errors.New("MCP HTTP endpoint requires an exact allowed_hosts list")
		}
		matched := false
		for _, host := range config.AllowedHosts {
			if !validHost(host) {
				return errors.New("MCP allowed_hosts entries must be exact host names")
			}
			if strings.EqualFold(host, parsed.Hostname()) {
				matched = true
			}
		}
		if !matched {
			return errors.New("MCP endpoint host is not in allowed_hosts")
		}
		if config.Command != "" || len(config.Args) > 0 || config.WorkingDir != "" || len(config.Environment) > 0 {
			return errors.New("stdio fields are not valid for HTTP MCP servers")
		}
	case TransportStdio:
		if !filepath.IsAbs(config.Command) || filepath.Clean(config.Command) != config.Command {
			return errors.New("MCP stdio command must be an absolute executable path")
		}
		base := strings.ToLower(filepath.Base(config.Command))
		switch base {
		case "sh", "bash", "zsh", "fish", "dash", "cmd.exe", "powershell.exe", "pwsh.exe":
			return errors.New("shell interpreters are not accepted as MCP stdio commands")
		}
		info, err := os.Stat(config.Command)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return errors.New("MCP stdio command must be an existing executable file")
		}
		if !filepath.IsAbs(config.WorkingDir) {
			return errors.New("MCP stdio working_dir must be absolute")
		}
		dir, err := os.Stat(config.WorkingDir)
		if err != nil || !dir.IsDir() {
			return errors.New("MCP stdio working_dir must be an existing directory")
		}
		if len(config.Args) > 128 {
			return errors.New("MCP stdio argument count exceeds limit")
		}
		for _, arg := range config.Args {
			if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
				return errors.New("MCP stdio argument is invalid")
			}
		}
		if len(config.Environment) > 64 {
			return errors.New("MCP stdio environment exceeds limit")
		}
		for key, value := range config.Environment {
			if !envNamePattern.MatchString(key) || strings.ContainsAny(value, "\x00\r\n") || len(value) > 8192 {
				return errors.New("MCP stdio environment entry is invalid")
			}
		}
		if config.Endpoint != "" || len(config.AllowedHosts) > 0 {
			return errors.New("HTTP fields are not valid for stdio MCP servers")
		}
	default:
		return errors.New("MCP transport must be streamable_http or stdio")
	}
	return nil
}

func validHost(host string) bool {
	if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "/@*\\\r\n\x00") || strings.HasSuffix(host, ".") || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-') {
				return false
			}
		}
	}
	return true
}

func safeHTTPClient(config ServerConfig) (*http.Client, error) {
	parsed, _ := url.Parse(config.Endpoint)
	allowed := map[string]bool{}
	for _, host := range config.AllowedHosts {
		allowed[strings.ToLower(host)] = true
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, IdleConnTimeout: 30 * time.Second, MaxIdleConnsPerHost: 4, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if !allowed[strings.ToLower(host)] {
			return nil, errors.New("MCP endpoint host is not allowed")
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, item := range ips {
			if !publicMCPAddress(item.IP, parsed.Scheme == "http") {
				continue
			}
			dialer := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(item.IP.String(), port))
			if dialErr == nil {
				return connection, nil
			}
		}
		return nil, errors.New("MCP endpoint resolved to a prohibited address")
	}}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func publicMCPAddress(ip net.IP, allowLoopback bool) bool {
	if ip == nil {
		return false
	}
	if allowLoopback && ip.IsLoopback() {
		return true
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		_, block, err := net.ParseCIDR(cidr)
		if err == nil && block.Contains(ip) {
			return false
		}
	}
	return true
}

// Ensure the only runtime implementation is the official SDK transport and client.
// 中文：编译期确保运行时只使用官方 SDK 提供的传输与客户端实现。
var _ sdk.Transport = (*sdk.StreamableClientTransport)(nil)
var _ sdk.Transport = (*sdk.CommandTransport)(nil)
