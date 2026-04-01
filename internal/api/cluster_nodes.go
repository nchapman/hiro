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

	// Add to approved nodes in config.
	s.cp.ApproveNode(nodeID, pending.Name)
	if err := s.cp.Save(); err != nil {
		s.logger.Error("failed to save config after approving node", "error", err)
		http.Error(w, "failed to save configuration", http.StatusInternalServerError)
		return
	}

	// Remove from pending.
	s.pendingRegistry.Remove(nodeID)

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
	if err := s.cp.Save(); err != nil {
		s.logger.Error("failed to save config after revoking node", "error", err)
		http.Error(w, "failed to save configuration", http.StatusInternalServerError)
		return
	}

	// Forcefully disconnect the node if it's currently connected.
	if s.disconnectNode != nil {
		s.disconnectNode(nodeID)
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
