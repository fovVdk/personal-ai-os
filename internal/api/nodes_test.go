// English: These tests verify nodes are registered offline until a separate heartbeat is received.
// 中文：本测试验证节点注册后保持离线状态，直至收到独立心跳。
// 中文：本测试验证节点注册后保持离线，必须另行收到心跳才可更新在线状态。
package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"personal-ai-os/internal/node"
	paierrors "personal-ai-os/pkg/errors"
)

type nodeMemory struct{ items map[string]node.Node }

func (m *nodeMemory) Register(_ context.Context, item node.Node) (node.Node, error) {
	item.ID = "node-test"
	item.Status = node.Offline
	m.items[item.ID] = item
	return item, nil
}
func (m *nodeMemory) List(context.Context, int) ([]node.Node, error) {
	items := make([]node.Node, 0, len(m.items))
	for _, item := range m.items {
		items = append(items, item)
	}
	return items, nil
}
func (m *nodeMemory) Get(_ context.Context, id string) (node.Node, error) {
	item, ok := m.items[id]
	if !ok {
		return node.Node{}, paierrors.ErrNotFound
	}
	return item, nil
}
func (m *nodeMemory) Heartbeat(_ context.Context, id string, status node.Status, at time.Time) (node.Node, error) {
	item, ok := m.items[id]
	if !ok {
		return node.Node{}, paierrors.ErrNotFound
	}
	item.Status = status
	item.LastHeartbeat = &at
	m.items[id] = item
	return item, nil
}

func TestNodeRegisterAPIStartsOffline(t *testing.T) {
	store := &nodeMemory{items: map[string]node.Node{}}
	handler := (&Server{Nodes: store}).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/nodes/register", strings.NewReader(`{"identity":"camera-node-1","name":"Vision Node","type":"vision","capabilities":["capture"]}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if item, _ := store.Get(context.Background(), "node-test"); item.Status != node.Offline {
		t.Fatalf("new node must begin offline: %+v", item)
	}
}
