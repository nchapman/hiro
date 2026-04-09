package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nchapman/hiro/internal/cluster"
	"github.com/nchapman/hiro/internal/config"
	"github.com/nchapman/hiro/internal/inference"
	"github.com/nchapman/hiro/internal/ipc"
	"github.com/nchapman/hiro/internal/models"
	"github.com/nchapman/hiro/internal/netiso"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
	"github.com/nchapman/hiro/internal/toolrules"
	"github.com/nchapman/hiro/internal/uidpool"
)

// InstanceStatus represents the lifecycle state of an instance.
type InstanceStatus string

const (
	InstanceStatusRunning InstanceStatus = "running"
	InstanceStatusStopped InstanceStatus = "stopped"
)

// InstanceInfo describes an agent instance for external consumers.
// Name and Description come from the agent definition but are overridden
// by persona.md frontmatter when present (resolved at listing time).
type InstanceInfo struct {
	ID          string
	Name        string // resolved: persona name > agent definition name
	Mode        config.AgentMode
	Description string // resolved: persona description > agent definition description
	ParentID    string // empty for top-level instances
	Status      InstanceStatus
	Model       string     // resolved model ID
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

// sessionSlot holds the per-session worker, loop, and handle. Each channel
// (web, telegram, agent, etc.) gets its own slot on the instance.
type sessionSlot struct {
	mu        sync.Mutex // serializes Chat calls on this session
	sessionID string
	channel   string          // compound key: "web", "tg:12345", "agent:<parentID>", "trigger:<subID>"
	worker    ipc.AgentWorker // worker process for tool execution
	handle    *WorkerHandle
	loop      *inference.Loop // inference loop (runs in control plane)
	lastUsed  time.Time       // for idle timeout
}

// instance tracks a live agent instance within the manager.
//
// Lock ordering: m.mu → inst.mu → slot.mu (no reverse path exists).
// m.mu protects the instances and children maps; inst.mu protects the
// sessions/channelIndex maps and instance-level state; slot.mu serializes
// Chat calls on individual sessions.
type instance struct {
	mu              sync.Mutex // protects sessions, channelIndex, and instance-level state
	info            InstanceInfo
	agentName       string                       // agent definition name (immutable, for config loading)
	sessions        map[string]*sessionSlot      // sessionID → slot
	channelIndex    map[string]string            // compound channel key → sessionID
	notifications   *inference.NotificationQueue // instance-level; survives session recreation
	memoryMu        *sync.Mutex                  // protects concurrent memory.md read-modify-write across sessions
	effectiveTools  map[string]bool              // built-in tools this instance is allowed; nil = unrestricted
	allowLayers     [][]toolrules.Rule           // per-source allow rules for call-time enforcement
	denyRules       []toolrules.Rule             // merged deny rules from all sources
	configGen       uint64                       // incremented on each model/tool config change under inst.mu
	effectiveEgress []string                     // effective network egress policy; always non-nil (empty = default-deny, no connectivity)
	uid             uint32                       // isolated UID (0 = no isolation)
	gid             uint32                       // isolated GID
	groups          []uint32                     // supplementary groups only (not primary GID); used for inheritance checks
	nodeID          ipc.NodeID                   // which node this instance runs on ("home" for local)
}

// slotByChannel returns the session slot for a compound channel key, or nil.
// Must be called with inst.mu held.
func (inst *instance) slotByChannel(channel string) *sessionSlot {
	if sid, ok := inst.channelIndex[channel]; ok {
		return inst.sessions[sid]
	}
	return nil
}

// addSlot registers a session slot in the instance maps.
// Must be called with inst.mu held.
func (inst *instance) addSlot(slot *sessionSlot) {
	inst.sessions[slot.sessionID] = slot
	inst.channelIndex[slot.channel] = slot.sessionID
}

// removeSlot removes a session slot from the instance maps.
// Must be called with inst.mu held.
func (inst *instance) removeSlot(sessionID string) {
	if slot, ok := inst.sessions[sessionID]; ok {
		delete(inst.channelIndex, slot.channel)
		delete(inst.sessions, sessionID)
	}
}

// anySlot returns an arbitrary live session slot, or nil if none exist.
// Must be called with inst.mu held.
func (inst *instance) anySlot() *sessionSlot {
	for _, slot := range inst.sessions {
		slot.mu.Lock()
		live := slot.loop != nil
		slot.mu.Unlock()
		if live {
			return slot
		}
	}
	return nil
}

// Manager supervises agent instance lifecycles on a single node.
type Manager struct {
	mu        sync.RWMutex
	instances map[string]*instance // instance ID -> running instance
	children  map[string][]string  // parent instance ID -> child instance IDs

	ctx     context.Context // long-lived context for persistent instances
	rootDir string
	opts    Options
	cp      ControlPlane // operator-level tool/secret config
	logger  *slog.Logger

	workerFactory  WorkerFactory               // creates agent workers (default = OS processes)
	netIso         *netiso.NetIso              // per-agent network isolation; nil = disabled
	uidPool        *uidpool.Pool               // per-agent Unix user isolation; nil = disabled
	pdb            *platformdb.DB              // unified platform database
	clusterService *cluster.LeaderService      // cluster orchestration; nil = standalone
	scheduler      *Scheduler                  // cron scheduler; nil until SetScheduler called
	timezone       *time.Location              // server timezone for cron evaluation
	lifecycleHook  InstanceLifecycleHook       // optional hook for instance start/stop events
	configLocker   config.InstanceConfigLocker // serializes config.yaml read-modify-write; nil = no locking
}

// InstanceLifecycleHook is called when instances start or stop, allowing
// external systems (e.g. channel management) to react without creating
// import cycles between the agent and channel packages.
type InstanceLifecycleHook interface {
	// OnInstanceStart is called after an instance is fully registered and running.
	// instDir is the instance's filesystem directory.
	OnInstanceStart(ctx context.Context, instanceID, instDir string) error

	// OnInstanceStop is called when an instance is being stopped or removed.
	// Must be idempotent — may be called multiple times for the same instance
	// (e.g. softStop followed by removeInstance during delete).
	OnInstanceStop(instanceID string)
}

// ControlPlane is the interface the Manager uses for operator-level config.
// Defined here to avoid a direct dependency on the controlplane package.
type ControlPlane interface {
	SecretNames() []string
	SecretEnv() []string
	ProviderInfo() (providerType string, apiKey string, baseURL string, ok bool)
	ProviderByType(providerType string) (apiKey string, baseURL string, ok bool)
	ConfiguredProviderTypes() []string
	DefaultModelSpec() models.ModelSpec
	ResolveSecret(value string) string
}

// NewManager creates a new agent manager. rootDir is the hiro platform root
// containing agents/, instances/, skills/, and workspace/ subdirectories. The context
// controls the lifetime of persistent instances. cp may be nil if no control
// plane is configured. If wf is nil, the default OS process spawner is used.
// ni may be nil if network isolation is not available.
func NewManager(ctx context.Context, rootDir string, opts Options, cp ControlPlane, logger *slog.Logger, wf WorkerFactory, pool *uidpool.Pool, pdb *platformdb.DB, ni *netiso.NetIso) *Manager {
	if wf == nil {
		if ni != nil {
			wf = newWorkerFactory(ni)
		} else {
			wf = defaultWorkerFactory
		}
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
		netIso:        ni,
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
	result := ipc.InstanceInfo{
		ID:          info.ID,
		Name:        info.Name,
		Mode:        string(info.Mode),
		Description: info.Description,
		ParentID:    info.ParentID,
		Status:      string(info.Status),
		Model:       info.Model,
	}
	// Include effective egress if the instance is known.
	m.mu.RLock()
	if inst, ok := m.instances[info.ID]; ok {
		result.EffectiveEgress = inst.effectiveEgress
	}
	m.mu.RUnlock()
	return result
}
