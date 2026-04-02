package api

import (
	"github.com/nchapman/hiro/internal/cluster"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

// clusterTerminalSender implements RemoteTerminalSender using a LeaderService.
type clusterTerminalSender struct {
	svc *cluster.LeaderService
}

func (s *clusterTerminalSender) SendCreateTerminal(nodeID string, sessionID string, cols, rows uint32) error {
	return s.svc.SendTerminalMessage(cluster.NodeID(nodeID), &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_CreateTerminal{
			CreateTerminal: &pb.CreateTerminal{
				SessionId: sessionID,
				Cols:      cols,
				Rows:      rows,
			},
		},
	})
}

func (s *clusterTerminalSender) SendTerminalInput(nodeID string, sessionID string, data []byte) error {
	return s.svc.SendTerminalMessage(cluster.NodeID(nodeID), &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_TerminalInput{
			TerminalInput: &pb.TerminalInput{
				SessionId: sessionID,
				Data:      data,
			},
		},
	})
}

func (s *clusterTerminalSender) SendTerminalResize(nodeID string, sessionID string, cols, rows uint32) error {
	return s.svc.SendTerminalMessage(cluster.NodeID(nodeID), &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_TerminalResize{
			TerminalResize: &pb.TerminalResize{
				SessionId: sessionID,
				Cols:      cols,
				Rows:      rows,
			},
		},
	})
}

func (s *clusterTerminalSender) SendCloseTerminal(nodeID string, sessionID string) error {
	return s.svc.SendTerminalMessage(cluster.NodeID(nodeID), &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_CloseTerminal{
			CloseTerminal: &pb.CloseTerminal{
				SessionId: sessionID,
			},
		},
	})
}

// clusterNodeChecker implements NodeChecker using the cluster NodeRegistry.
type clusterNodeChecker struct {
	registry *cluster.NodeRegistry
}

func (c *clusterNodeChecker) IsOnlineApproved(nodeID string) bool {
	info, ok := c.registry.Get(cluster.NodeID(nodeID))
	return ok && info.Status == cluster.NodeOnline
}

// WireClusterTerminal connects the terminal session manager to a cluster
// LeaderService for remote terminal support.
func WireClusterTerminal(termSessions *TerminalSessionManager, svc *cluster.LeaderService) {
	termSessions.SetRemote(&clusterTerminalSender{svc: svc})
	termSessions.SetNodeChecker(&clusterNodeChecker{registry: svc.Registry()})

	svc.SetTerminalHandlers(
		func(nodeID cluster.NodeID, msg *pb.TerminalCreated) {
			termSessions.HandleTerminalCreated(string(nodeID), msg.SessionId, msg.Error)
		},
		func(nodeID cluster.NodeID, msg *pb.TerminalOutput) {
			termSessions.HandleTerminalOutput(string(nodeID), msg.SessionId, msg.Data)
		},
		func(nodeID cluster.NodeID, msg *pb.TerminalExited) {
			termSessions.HandleTerminalExited(string(nodeID), msg.SessionId, int(msg.ExitCode))
		},
	)
}
