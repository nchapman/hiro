package api

import (
	"net/http"

	"github.com/nchapman/hiro/internal/cluster"
)

// SetPendingRegistry sets the pending node registry for the approval endpoints.
func (s *Server) SetPendingRegistry(pr *cluster.PendingRegistry) {
	s.pendingRegistry = pr
}

// SetWorkerStatus sets a function that returns the worker's connection status.
func (s *Server) SetWorkerStatus(fn func() string) {
	s.workerStatus = fn
}

// SetDisconnectNode sets a function to forcefully disconnect a node.
func (s *Server) SetDisconnectNode(fn func(string)) {
	s.disconnectNode = fn
}

// handleListPending returns all nodes awaiting approval.
// GET /api/cluster/pending
func (s *Server) handleListPending(w http.ResponseWriter, _ *http.Request) {
	if s.pendingRegistry == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, s.pendingRegistry.List())
}

// handleApproveNode approves a pending node, moving it to the approved list.
// POST /api/cluster/pending/{nodeID}/approve
func (s *Server) handleApproveNode(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("nodeID")
	if nodeID == "" {
		http.Error(w, "nodeID is required", http.StatusBadRequest)
		return
	}

	if s.cp == nil || s.pendingRegistry == nil {
		http.Error(w, "cluster not configured", http.StatusServiceUnavailable)
		return
	}

	// Look up the pending node to get its name.
	pending, ok := s.pendingRegistry.Get(nodeID)
	if !ok {
		http.Error(w, "node not found in pending list", http.StatusNotFound)
		return
	}

	// Enforce first, persist second — remove from pending and add to approved
	// before saving, so the node is never in both lists even if save fails.
	s.pendingRegistry.Remove(nodeID)
	s.cp.ApproveNode(nodeID, pending.Name)
	if err := s.cp.Save(); err != nil {
		s.logger.Error("node approved in memory but config save failed", "error", err)
		http.Error(w, "node approved but config save failed; approval may not survive restart", http.StatusInternalServerError)
		return
	}

	s.logger.Info("node approved", "node_id", nodeID, "name", pending.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDismissNode removes a node from the pending list without approving it.
// DELETE /api/cluster/pending/{nodeID}
func (s *Server) handleDismissNode(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("nodeID")
	if nodeID == "" {
		http.Error(w, "nodeID is required", http.StatusBadRequest)
		return
	}

	if s.pendingRegistry == nil {
		http.Error(w, "cluster not configured", http.StatusServiceUnavailable)
		return
	}

	s.pendingRegistry.Remove(nodeID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleListApproved returns all approved nodes from config.
// GET /api/cluster/approved
func (s *Server) handleListApproved(w http.ResponseWriter, _ *http.Request) {
	if s.cp == nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	nodes := s.cp.ApprovedNodes()
	if nodes == nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	writeJSON(w, http.StatusOK, nodes)
}

// handleRemoveApproved revokes a node's approval.
// DELETE /api/cluster/approved/{nodeID}
func (s *Server) handleRemoveApproved(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("nodeID")
	if nodeID == "" {
		http.Error(w, "nodeID is required", http.StatusBadRequest)
		return
	}
	if s.cp == nil {
		http.Error(w, "cluster not configured", http.StatusServiceUnavailable)
		return
	}

	s.cp.RevokeNode(nodeID)

	// Always enforce revocation immediately — disconnect and unregister before
	// attempting to persist. A revoked node must not stay connected even if the
	// config write fails. DisconnectNode closes the gRPC stream, which causes
	// cleanupNode to call SetOffline (not Unregister) asynchronously. We call
	// Unregister here synchronously so the node disappears from the UI immediately.
	if s.disconnectNode != nil {
		s.disconnectNode(nodeID)
	}
	if s.nodeRegistry != nil {
		s.nodeRegistry.Unregister(nodeID)
	}

	if err := s.cp.Save(); err != nil {
		s.logger.Error("node revoked and disconnected but config save failed — revocation will not survive restart",
			"node_id", nodeID, "error", err)
		http.Error(w, "node disconnected but config save failed; revocation may not survive restart", http.StatusInternalServerError)
		return
	}

	s.logger.Info("node revoked", "node_id", nodeID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleClearRevoked removes a node from the revoked list, allowing it to
// reconnect and appear as pending again.
// DELETE /api/cluster/revoked/{nodeID}
func (s *Server) handleClearRevoked(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("nodeID")
	if nodeID == "" {
		http.Error(w, "nodeID is required", http.StatusBadRequest)
		return
	}
	if s.cp == nil {
		http.Error(w, "cluster not configured", http.StatusServiceUnavailable)
		return
	}

	s.cp.ClearRevokedNode(nodeID)
	if err := s.cp.Save(); err != nil {
		s.logger.Error("failed to save config after clearing revoked node", "error", err)
		http.Error(w, "failed to save configuration", http.StatusInternalServerError)
		return
	}

	s.logger.Info("revocation cleared", "node_id", nodeID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
