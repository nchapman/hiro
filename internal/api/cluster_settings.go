package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/nchapman/hiro/internal/cluster"
	"github.com/nchapman/hiro/internal/controlplane"
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
	case roleLeader:
		s.populateLeaderClusterSettings(resp)
	case roleWorker:
		s.populateWorkerClusterSettings(resp)
	}

	writeJSON(w, http.StatusOK, resp)
}

// populateLeaderClusterSettings adds leader-specific fields to the cluster
// settings response.
func (s *Server) populateLeaderClusterSettings(resp map[string]any) {
	resp["swarm_code"] = s.cp.ClusterSwarmCode()
	resp["tracker_url"] = s.cp.ClusterTrackerURL()
	if addrs := s.cp.ClusterAdvertiseAddresses(); len(addrs) > 0 {
		resp["advertise_addresses"] = addrs
	} else {
		resp["advertise_addresses"] = []string{}
	}

	// Include pending node count for dashboard badge.
	if s.pendingRegistry != nil {
		resp["pending_count"] = s.pendingRegistry.Count()
	}

	approved := s.cp.ApprovedNodes()
	if len(approved) > 0 {
		resp["approved_nodes"] = approved
	}

	// Include connected worker nodes (only approved ones).
	if s.nodeRegistry != nil {
		resp["nodes"] = s.approvedNodeList(approved)
	}
}

// clusterNodeInfo is the JSON shape for node entries in cluster settings.
type clusterNodeInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	IsHome bool   `json:"is_home"`
	Via    string `json:"via,omitempty"`
}

// approvedNodeList returns the list of connected nodes filtered to only
// those in the approved set (plus the home node).
func (s *Server) approvedNodeList(approved map[string]controlplane.ApprovedNode) []clusterNodeInfo {
	nodes := s.nodeRegistry.List()
	list := make([]clusterNodeInfo, 0, len(nodes))
	for _, n := range nodes {
		// Filter non-home nodes by the approved list. When approved
		// is nil (no nodes have ever been approved), we skip the
		// filter — the registry will only contain the home node.
		if !n.IsHome && approved != nil {
			if _, ok := approved[n.ID]; !ok {
				continue
			}
		}
		list = append(list, clusterNodeInfo{
			ID:     n.ID,
			Name:   n.Name,
			Status: string(n.Status),
			IsHome: n.IsHome,
			Via:    n.Via,
		})
	}
	return list
}

// populateWorkerClusterSettings adds worker-specific fields to the cluster
// settings response.
func (s *Server) populateWorkerClusterSettings(resp map[string]any) {
	resp["leader_addr"] = s.cp.ClusterLeaderAddr()
	resp["swarm_code"] = s.cp.ClusterSwarmCode()
	if s.workerStatus != nil {
		resp["connection_status"] = s.workerStatus()
	}
}

// handleUpdateClusterAdvertise replaces the leader's advertise addresses.
// PUT /api/settings/cluster/advertise
//
// Body: {"addresses": ["tcp://1.2.3.4:5000", ...]}. Pass an empty list to
// clear the override and fall back to tracker-observed source IP.
//
// Addresses are validated with cluster.ValidateAdvertiseAddress and the
// server is restarted so the discovery client picks up the new list.
func (s *Server) handleUpdateClusterAdvertise(w http.ResponseWriter, r *http.Request) {
	if s.cp == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	if s.cp.ClusterMode() != roleLeader {
		http.Error(w, "advertise addresses only apply in leader mode", http.StatusBadRequest)
		return
	}

	var body struct {
		Addresses []string `json:"addresses"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodySize)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if len(body.Addresses) > cluster.MaxAdvertiseAddresses {
		http.Error(w, fmt.Sprintf("too many addresses (max %d)", cluster.MaxAdvertiseAddresses), http.StatusBadRequest)
		return
	}

	cleaned := make([]string, 0, len(body.Addresses))
	for _, a := range body.Addresses {
		if a == "" {
			continue
		}
		if msg := cluster.ValidateAdvertiseAddress(a); msg != "" {
			http.Error(w, "invalid address: "+msg, http.StatusBadRequest)
			return
		}
		cleaned = append(cleaned, a)
	}

	s.cp.SetClusterAdvertiseAddresses(cleaned)
	if err := s.cp.Save(); err != nil {
		s.logger.Error("failed to save advertise addresses", "error", err)
		http.Error(w, "failed to save configuration", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                  true,
		"advertise_addresses": cleaned,
	})

	if s.requestRestart != nil {
		go func() {
			time.Sleep(restartDelay)
			s.requestRestart()
		}()
	}
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
			time.Sleep(restartDelay)
			s.requestRestart()
		}()
	}
}
