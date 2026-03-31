package agent

import (
	"context"
	"log/slog"
	"sync"

	"github.com/nchapman/hivebot/internal/cluster"
	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/inference"
	"github.com/nchapman/hivebot/internal/ipc"
	platformdb "github.com/nchapman/hivebot/internal/platform/db"
	"github.com/nchapman/hivebot/internal/uidpool"
)

// InstanceStatus represents the lifecycle state of an instance.
type InstanceStatus string

const (
	InstanceStatusRunning InstanceStatus = "running"
	InstanceStatusStopped InstanceStatus = "stopped"
)

// InstanceInfo describes an agent instance for external consumers.
type InstanceInfo struct {
	ID          string
	Name        string
	Mode        config.AgentMode
	Description string
	ParentID    string         // empty for top-level instances
	Status      InstanceStatus
	Model       string         // resolved model ID
	NodeID      ipc.NodeID // which node this instance runs on
}

// WorkerHandle represents a running agent worker (process or mock).
type WorkerHandle struct {
	Worker ipc.AgentWorker
	Kill   func()          // force-kill the process (SIGKILL)
	Close  func()          // close gRPC conn, remove socket, etc.
	Done   <-chan struct{} // closed when the worker exits
}

// WorkerFactory creates agent workers. The default implementation spawns
// OS processes. Tests inject alternatives that return fake workers.
type WorkerFactory func(ctx context.Context, cfg ipc.SpawnConfig) (*WorkerHandle, error)

// instance tracks a live agent instance within the manager.
//
// Lock ordering: m.mu → inst.mu (no reverse path exists in the codebase).
// m.mu protects the instances and children maps; inst.mu serializes calls
// through the worker (SendMessage, UpdateConfig) and protects mutable
// per-instance state (handle, loop, status).
type instance struct {
	mu             sync.Mutex // serializes calls through the worker
	info           InstanceInfo
	activeSession  string             // current session ID
	worker         ipc.AgentWorker
	handle         *WorkerHandle
	loop           *inference.Loop    // inference loop (runs in control plane)
	notifications  *inference.NotificationQueue // instance-level; survives loop recreation
	effectiveTools map[string]bool    // built-in tools this instance is allowed; nil = unrestricted
	uid            uint32             // isolated UID (0 = no isolation)
	gid            uint32             // isolated GID
	groups         []uint32           // supplementary groups (includes hive-coordinators for coordinators)
	nodeID         ipc.NodeID         // which node this instance runs on ("home" for local)
}

// Manager supervises agent instance lifecycles on a single node.
type Manager struct {
	mu        sync.RWMutex
	instances map[string]*instance  // instance ID -> running instance
	children  map[string][]string   // parent instance ID -> child instance IDs

	ctx     context.Context // long-lived context for persistent instances
	rootDir string
	opts    Options
	cp      ControlPlane // operator-level tool/secret config
	logger  *slog.Logger

	workerFactory  WorkerFactory          // creates agent workers (default = OS processes)
	uidPool        *uidpool.Pool          // per-agent Unix user isolation; nil = disabled
	pdb            *platformdb.DB         // unified platform database
	clusterService *cluster.LeaderService // cluster orchestration; nil = standalone
}

// ControlPlane is the interface the Manager uses for operator-level config.
// Defined here to avoid a direct dependency on the controlplane package.
type ControlPlane interface {
	AgentTools(name string) (tools []string, ok bool)
	SecretNames() []string
	SecretEnv() []string
	ProviderInfo() (providerType string, apiKey string, baseURL string, ok bool)
	ProviderByType(providerType string) (apiKey string, baseURL string, ok bool)
	ConfiguredProviderTypes() []string
	DefaultModel() string
}

// NewManager creates a new agent manager. rootDir is the hive platform root
// containing agents/, instances/, skills/, and workspace/ subdirectories. The context
// controls the lifetime of persistent instances. cp may be nil if no control
// plane is configured. If wf is nil, the default OS process spawner is used.
func NewManager(ctx context.Context, rootDir string, opts Options, cp ControlPlane, logger *slog.Logger, wf WorkerFactory, pool *uidpool.Pool, pdb *platformdb.DB) *Manager {
	if wf == nil {
		wf = defaultWorkerFactory
	}
	return &Manager{
		instances:     make(map[string]*instance),
		children:      make(map[string][]string),
		ctx:           ctx,
		rootDir:       rootDir,
		opts:          opts,
		cp:            cp,
		logger:        logger.With("component", "agent"),
		workerFactory: wf,
		uidPool:       pool,
		pdb:           pdb,
	}
}

// getInstance returns the instance for the given ID, or nil.
func (m *Manager) getInstance(id string) *instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.instances[id]
}

// unregisterLocked removes an instance from the registry and its parent's children
// list. Must be called with m.mu held.
func (m *Manager) unregisterLocked(id string, inst *instance) {
	delete(m.instances, id)
	delete(m.children, id)
	if inst != nil && inst.info.ParentID != "" {
		siblings := m.children[inst.info.ParentID]
		updated := make([]string, 0, len(siblings))
		for _, cid := range siblings {
			if cid != id {
				updated = append(updated, cid)
			}
		}
		m.children[inst.info.ParentID] = updated
	}
}

// instanceInfoToIPC converts an InstanceInfo to ipc.InstanceInfo.
func (m *Manager) instanceInfoToIPC(info InstanceInfo) ipc.InstanceInfo {
	return ipc.InstanceInfo{
		ID:          info.ID,
		Name:        info.Name,
		Mode:        string(info.Mode),
		Description: info.Description,
		ParentID:    info.ParentID,
		Status:      string(info.Status),
		Model:       info.Model,
	}
}
