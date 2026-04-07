package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nchapman/hiro/internal/channel"
	"github.com/nchapman/hiro/internal/config"
)

// newChannelAccessTestServer creates a test server with an access checker wired in.
func newChannelAccessTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	srv, mgr, agentName := newInstanceTestServer(t)

	parentID, err := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("CreateInstance parent: %v", err)
	}
	instID, err := mgr.CreateInstance(context.Background(), agentName, parentID, "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	mgr.StopInstance(instID)

	ac := channel.NewConfigAccessChecker(mgr, srv.logger)
	srv.accessChecker = ac

	return srv, instID
}

// seedSender writes a sender entry directly to the instance config.
func seedSender(t *testing.T, srv *Server, instID, key string, status config.ChannelAccessStatus) {
	t.Helper()
	instDir := srv.manager.InstanceDir(instID)
	cfg, err := config.LoadInstanceConfig(instDir)
	if err != nil {
		t.Fatalf("LoadInstanceConfig: %v", err)
	}
	if cfg.Channels == nil {
		cfg.Channels = &config.InstanceChannelsConfig{}
	}
	cfg.Channels.SetSender(key, status, "Test User", "hello")
	if err := config.SaveInstanceConfig(instDir, cfg); err != nil {
		t.Fatalf("SaveInstanceConfig: %v", err)
	}
}

func TestListChannelAccess_Empty(t *testing.T) {
	srv, instID := newChannelAccessTestServer(t)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/instances/"+instID+"/channel-access", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var senders []channelSenderJSON
	json.NewDecoder(rec.Body).Decode(&senders)
	if len(senders) != 0 {
		t.Errorf("expected empty list, got %d senders", len(senders))
	}
}

func TestListChannelAccess_WithSenders(t *testing.T) {
	srv, instID := newChannelAccessTestServer(t)

	seedSender(t, srv, instID, "tg:12345", config.ChannelAccessPending)
	seedSender(t, srv, instID, "slack:C999", config.ChannelAccessApproved)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/instances/"+instID+"/channel-access", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	var senders []channelSenderJSON
	json.NewDecoder(rec.Body).Decode(&senders)
	if len(senders) != 2 {
		t.Fatalf("expected 2 senders, got %d", len(senders))
	}
}

func TestApproveChannelSender(t *testing.T) {
	srv, instID := newChannelAccessTestServer(t)
	seedSender(t, srv, instID, "tg:12345", config.ChannelAccessPending)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/instances/"+instID+"/channel-access/tg%3A12345/approve", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Verify the sender is now approved.
	instDir := srv.manager.InstanceDir(instID)
	cfg, _ := config.LoadInstanceConfig(instDir)
	status, found := cfg.Channels.SenderStatus("tg:12345")
	if !found || status != config.ChannelAccessApproved {
		t.Errorf("sender status=%v found=%v, want approved", status, found)
	}
}

func TestBlockChannelSender(t *testing.T) {
	srv, instID := newChannelAccessTestServer(t)
	seedSender(t, srv, instID, "tg:12345", config.ChannelAccessPending)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/instances/"+instID+"/channel-access/tg%3A12345/block", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	instDir := srv.manager.InstanceDir(instID)
	cfg, _ := config.LoadInstanceConfig(instDir)
	status, found := cfg.Channels.SenderStatus("tg:12345")
	if !found || status != config.ChannelAccessBlocked {
		t.Errorf("sender status=%v found=%v, want blocked", status, found)
	}
}

func TestDismissChannelSender(t *testing.T) {
	srv, instID := newChannelAccessTestServer(t)
	seedSender(t, srv, instID, "tg:12345", config.ChannelAccessPending)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/instances/"+instID+"/channel-access/tg%3A12345", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Verify the sender is gone.
	instDir := srv.manager.InstanceDir(instID)
	cfg, _ := config.LoadInstanceConfig(instDir)
	if _, found := cfg.Channels.SenderStatus("tg:12345"); found {
		t.Error("sender should be removed after dismiss")
	}
}

func TestDismissChannelSender_NotFound(t *testing.T) {
	srv, instID := newChannelAccessTestServer(t)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/instances/"+instID+"/channel-access/tg%3A99999", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestApproveChannelSender_NotFound(t *testing.T) {
	srv, instID := newChannelAccessTestServer(t)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/instances/"+instID+"/channel-access/tg%3A99999/approve", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestGlobalPendingChannelAccess_Empty(t *testing.T) {
	srv, _ := newChannelAccessTestServer(t)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/channel-access/pending", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	var result struct {
		Count int                 `json:"count"`
		Items []channelSenderJSON `json:"items"`
	}
	json.NewDecoder(rec.Body).Decode(&result)
	if result.Count != 0 {
		t.Errorf("expected 0 pending, got %d", result.Count)
	}
}

func TestGlobalPendingChannelAccess_WithPending(t *testing.T) {
	srv, instID := newChannelAccessTestServer(t)
	seedSender(t, srv, instID, "tg:111", config.ChannelAccessPending)
	seedSender(t, srv, instID, "tg:222", config.ChannelAccessApproved) // not pending
	seedSender(t, srv, instID, "slack:C333", config.ChannelAccessPending)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/channel-access/pending", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	var result struct {
		Count int                 `json:"count"`
		Items []channelSenderJSON `json:"items"`
	}
	json.NewDecoder(rec.Body).Decode(&result)
	if result.Count != 2 {
		t.Errorf("expected 2 pending, got %d", result.Count)
	}
	for _, item := range result.Items {
		if item.InstanceID != instID {
			t.Errorf("expected instance_id=%s, got %s", instID, item.InstanceID)
		}
	}
}

func TestApproveChannelSender_NoManager(t *testing.T) {
	srv := newTestServer()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/instances/x/channel-access/tg%3A1/approve", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", rec.Code)
	}
}
