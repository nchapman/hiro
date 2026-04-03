package api

import (
	"net/http"
	"time"

	"github.com/nchapman/hiro/internal/cluster"
)

// SetNodeRegistry sets the cluster node registry for the cluster status endpoint.
func (s *Server) SetNodeRegistry(nr *cluster.NodeRegistry) {
	s.nodeRegistry = nr
}

func (s *Server) handleGetClusterSettings(w http.ResponseWriter, _ *http.Request) {
	if s.cp == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}

	mode := s.cp.ClusterMode()
	resp := map[string]any{
		"mode":      mode,
		"node_name": s.cp.ClusterNodeName(),
	}

	switch mode {
	case "leader":
		resp["swarm_code"] = s.cp.ClusterSwarmCode()
		resp["tracker_url"] = s.cp.ClusterTrackerURL()

		// Include pending node count for dashboard badge.
		if s.pendingRegistry != nil {
			resp["pending_count"] = s.pendingRegistry.Count()
		}

		approved := s.cp.ApprovedNodes()

		// Include approved nodes.
		if len(approved) > 0 {
			resp["approved_nodes"] = approved
		}

		// Include connected worker nodes (only approved ones).
		if s.nodeRegistry != nil {
			nodes := s.nodeRegistry.List()
			type nodeInfo struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Status string `json:"status"`
				IsHome bool   `json:"is_home"`
				Via    string `json:"via,omitempty"`
			}
			nodeList := make([]nodeInfo, 0, len(nodes))
			for _, n := range nodes {
				// Filter non-home nodes by the approved list. When approved
				// is nil (no nodes have ever been approved), we skip the
				// filter — the registry will only contain the home node.
				if !n.IsHome && approved != nil {
					if _, ok := approved[n.ID]; !ok {
						continue
					}
				}
				nodeList = append(nodeList, nodeInfo{
					ID:     n.ID,
					Name:   n.Name,
					Status: string(n.Status),
					IsHome: n.IsHome,
					Via:    n.Via,
				})
			}
			resp["nodes"] = nodeList
		}

	case "worker":
		resp["leader_addr"] = s.cp.ClusterLeaderAddr()
		resp["swarm_code"] = s.cp.ClusterSwarmCode()
		if s.workerStatus != nil {
			resp["connection_status"] = s.workerStatus()
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleClusterReset clears cluster configuration and triggers a restart.
// POST /api/settings/cluster/reset
func (s *Server) handleClusterReset(w http.ResponseWriter, r *http.Request) {
	if s.cp == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}

	// Disconnect all connected nodes before wiping state.
	if s.nodeRegistry != nil {
		for _, n := range s.nodeRegistry.List() {
			if !n.IsHome && s.disconnectNode != nil {
				s.disconnectNode(n.ID)
			}
		}
		s.nodeRegistry.ClearRemote()
	}

	// Clear pending nodes and their backing file.
	if s.pendingRegistry != nil {
		s.pendingRegistry.Clear()
	}

	if err := s.cp.Reset(); err != nil {
		s.logger.Error("failed to reset config", "error", err)
		http.Error(w, "failed to reset configuration", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	if s.requestRestart != nil {
		go func() {
			time.Sleep(100 * time.Millisecond)
			s.requestRestart()
		}()
	}
}
