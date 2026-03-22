// Package uidpool manages a pool of pre-created Unix user IDs for agent
// process isolation. Each agent process is assigned a unique UID from the
// pool so it cannot access other agents' session data.
package uidpool

import (
	"fmt"
	"sync"
)

const (
	// DefaultBaseUID is the first UID in the agent user pool.
	DefaultBaseUID uint32 = 10000
	// DefaultSize is the number of users in the pool.
	DefaultSize = 64
)

// Pool tracks which UIDs from a pre-created range are currently in use.
// It is pure bookkeeping — no OS calls. The actual UID assignment happens
// via syscall.SysProcAttr.Credential at process spawn time.
type Pool struct {
	mu      sync.Mutex
	baseUID uint32
	gid     uint32            // GID of the hive-agents group
	size    int               // number of UIDs in the pool
	inUse   map[uint32]string // UID -> session ID
}

// New creates a UID pool starting at baseUID with the given group ID and size.
func New(baseUID, gid uint32, size int) *Pool {
	return &Pool{
		baseUID: baseUID,
		gid:     gid,
		size:    size,
		inUse:   make(map[uint32]string),
	}
}

// Acquire assigns the next available UID to the given session.
// Returns the UID, GID, and an error if the pool is exhausted.
func (p *Pool) Acquire(sessionID string) (uid, gid uint32, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := 0; i < p.size; i++ {
		candidate := p.baseUID + uint32(i)
		if _, taken := p.inUse[candidate]; !taken {
			p.inUse[candidate] = sessionID
			return candidate, p.gid, nil
		}
	}
	return 0, 0, fmt.Errorf("UID pool exhausted (all %d UIDs in use)", p.size)
}

// Release frees the UID associated with the given session ID.
// It is safe to call with a session ID that was never acquired.
func (p *Pool) Release(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for uid, sid := range p.inUse {
		if sid == sessionID {
			delete(p.inUse, uid)
			return
		}
	}
}

// InUse returns the number of UIDs currently assigned.
func (p *Pool) InUse() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.inUse)
}
