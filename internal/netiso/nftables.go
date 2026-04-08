//go:build linux

package netiso

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// firewall manages the nftables rules for agent network isolation.
type firewall struct {
	mu     sync.Mutex
	conn   *nftables.Conn
	table  *nftables.Table
	fwdCh  *nftables.Chain // base forward chain
	natCh  *nftables.Chain // base postrouting NAT chain
	agents map[string]*agentFW // session prefix → per-agent chains/sets
	logger *slog.Logger
}

// agentFW holds the nftables resources for a single agent.
type agentFW struct {
	chain *nftables.Chain
	ipSet *nftables.Set
}

// newFirewall initializes the nftables table with base chains.
func newFirewall(logger *slog.Logger) (*firewall, error) {
	conn, err := nftables.New()
	if err != nil {
		return nil, fmt.Errorf("creating nftables conn: %w", err)
	}

	// Create table.
	table := conn.AddTable(&nftables.Table{
		Family: nftables.TableFamilyINet,
		Name:   "hiro",
	})

	// Base forward chain (policy drop).
	fwdCh := conn.AddChain(&nftables.Chain{
		Name:     "forward",
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookForward,
		Priority: nftables.ChainPriorityFilter,
		Policy:   policyPtr(nftables.ChainPolicyDrop),
	})

	// Base postrouting NAT chain (MASQUERADE for agent subnets).
	natCh := conn.AddChain(&nftables.Chain{
		Name:     "postrouting",
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookPostrouting,
		Priority: nftables.ChainPriorityNATSource,
	})

	// MASQUERADE for 10.0.0.0/16 source traffic.
	conn.AddRule(&nftables.Rule{
		Table: table,
		Chain: natCh,
		Exprs: []expr.Any{
			// Load source IP.
			&expr.Payload{
				DestRegister: 1,
				Base:         expr.PayloadBaseNetworkHeader,
				Offset:       12,
				Len:          4,
			},
			// Compare against 10.0.0.0/16.
			&expr.Bitwise{
				SourceRegister: 1,
				DestRegister:   1,
				Len:            4,
				Mask:           net.CIDRMask(16, 32),
				Xor:            []byte{0, 0, 0, 0},
			},
			&expr.Cmp{
				Op:       expr.CmpOpEq,
				Register: 1,
				Data:     net.IPv4(10, 0, 0, 0).To4(),
			},
			&expr.Masq{},
		},
	})

	if err := conn.Flush(); err != nil {
		return nil, fmt.Errorf("flushing base nftables rules: %w", err)
	}

	return &firewall{
		conn:   conn,
		table:  table,
		fwdCh:  fwdCh,
		natCh:  natCh,
		agents: make(map[string]*agentFW),
		logger: logger,
	}, nil
}

// setupAgent creates per-agent nftables chain and IP set.
func (fw *firewall) setupAgent(agent AgentNetwork, wildcard bool) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	prefix := agent.SessionPrefix()
	gwIP := agent.GatewayIP().To4()
	agentSubnet := net.IPNet{
		IP:   net.IPv4(10, 0, byte(agent.AgentID), 0),
		Mask: net.CIDRMask(30, 32),
	}

	// Create dynamic IP set with timeout support.
	ipSet := &nftables.Set{
		Table:   fw.table,
		Name:    prefix + "_ips",
		KeyType: nftables.TypeIPAddr,
		HasTimeout: true,
	}
	if err := fw.conn.AddSet(ipSet, nil); err != nil {
		return fmt.Errorf("adding IP set %s: %w", ipSet.Name, err)
	}

	// Per-agent chain.
	chain := fw.conn.AddChain(&nftables.Chain{
		Name:  prefix,
		Table: fw.table,
	})

	if wildcard {
		// Wildcard agent: accept all traffic.
		fw.conn.AddRule(&nftables.Rule{
			Table: fw.table,
			Chain: chain,
			Exprs: []expr.Any{
				&expr.Verdict{Kind: expr.VerdictAccept},
			},
		})
	} else {
		// Rule 1: Allow established/related connections.
		fw.conn.AddRule(&nftables.Rule{
			Table: fw.table,
			Chain: chain,
			Exprs: []expr.Any{
				&expr.Ct{Register: 1, SourceRegister: false, Key: expr.CtKeySTATE},
				&expr.Bitwise{
					SourceRegister: 1,
					DestRegister:   1,
					Len:            4,
					Mask:           binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED),
					Xor:            []byte{0, 0, 0, 0},
				},
				&expr.Cmp{
					Op:       expr.CmpOpNeq,
					Register: 1,
					Data:     []byte{0, 0, 0, 0},
				},
				&expr.Verdict{Kind: expr.VerdictAccept},
			},
		})

		// Rule 2: Allow DNS to gateway (UDP + TCP port 53).
		for _, proto := range []byte{unix.IPPROTO_TCP, unix.IPPROTO_UDP} {
			fw.conn.AddRule(&nftables.Rule{
				Table: fw.table,
				Chain: chain,
				Exprs: []expr.Any{
					// Match destination IP = gateway.
					&expr.Payload{
						DestRegister: 1,
						Base:         expr.PayloadBaseNetworkHeader,
						Offset:       16, // dst IP
						Len:          4,
					},
					&expr.Cmp{
						Op:       expr.CmpOpEq,
						Register: 1,
						Data:     gwIP,
					},
					// Match protocol.
					&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
					&expr.Cmp{
						Op:       expr.CmpOpEq,
						Register: 1,
						Data:     []byte{proto},
					},
					// Match destination port 53.
					&expr.Payload{
						DestRegister: 1,
						Base:         expr.PayloadBaseTransportHeader,
						Offset:       2, // dst port
						Len:          2,
					},
					&expr.Cmp{
						Op:       expr.CmpOpEq,
						Register: 1,
						Data:     binaryutil.BigEndian.PutUint16(53),
					},
					&expr.Verdict{Kind: expr.VerdictAccept},
				},
			})
		}

		// Rule 3: Block mDNS (UDP to 224.0.0.251:5353).
		fw.conn.AddRule(&nftables.Rule{
			Table: fw.table,
			Chain: chain,
			Exprs: []expr.Any{
				&expr.Payload{
					DestRegister: 1,
					Base:         expr.PayloadBaseNetworkHeader,
					Offset:       16,
					Len:          4,
				},
				&expr.Cmp{
					Op:       expr.CmpOpEq,
					Register: 1,
					Data:     net.IPv4(224, 0, 0, 251).To4(),
				},
				&expr.Verdict{Kind: expr.VerdictDrop},
			},
		})

		// Rule 4: Allow traffic to DNS-resolved IPs (any port, any protocol).
		fw.conn.AddRule(&nftables.Rule{
			Table: fw.table,
			Chain: chain,
			Exprs: []expr.Any{
				&expr.Payload{
					DestRegister: 1,
					Base:         expr.PayloadBaseNetworkHeader,
					Offset:       16,
					Len:          4,
				},
				&expr.Lookup{
					SourceRegister: 1,
					SetName:        ipSet.Name,
					SetID:          ipSet.ID,
				},
				&expr.Verdict{Kind: expr.VerdictAccept},
			},
		})

		// Rule 5: Default deny with counter.
		fw.conn.AddRule(&nftables.Rule{
			Table: fw.table,
			Chain: chain,
			Exprs: []expr.Any{
				&expr.Counter{},
				&expr.Verdict{Kind: expr.VerdictDrop},
			},
		})
	}

	// Jump rule in forward chain: route agent subnet traffic to per-agent chain.
	fw.conn.AddRule(&nftables.Rule{
		Table: fw.table,
		Chain: fw.fwdCh,
		Exprs: []expr.Any{
			// Match source IP in agent's /30 subnet.
			&expr.Payload{
				DestRegister: 1,
				Base:         expr.PayloadBaseNetworkHeader,
				Offset:       12,
				Len:          4,
			},
			&expr.Bitwise{
				SourceRegister: 1,
				DestRegister:   1,
				Len:            4,
				Mask:           agentSubnet.Mask,
				Xor:            []byte{0, 0, 0, 0},
			},
			&expr.Cmp{
				Op:       expr.CmpOpEq,
				Register: 1,
				Data:     agentSubnet.IP.To4(),
			},
			&expr.Verdict{
				Kind:  expr.VerdictJump,
				Chain: chain.Name,
			},
		},
	})

	if err := fw.conn.Flush(); err != nil {
		return fmt.Errorf("flushing agent nftables rules: %w", err)
	}

	fw.agents[prefix] = &agentFW{
		chain: chain,
		ipSet: ipSet,
	}
	return nil
}

// addIPs adds resolved IPs to an agent's nftables set with the given TTL.
func (fw *firewall) addIPs(sessionPrefix string, ips []net.IP, ttl time.Duration) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	afw, ok := fw.agents[sessionPrefix]
	if !ok {
		return fmt.Errorf("no agent with session prefix %q", sessionPrefix)
	}

	// Enforce minimum TTL.
	if ttl < MinTTL {
		ttl = MinTTL
	}

	elements := make([]nftables.SetElement, 0, len(ips))
	for _, ip := range ips {
		ip4 := ip.To4()
		if ip4 == nil {
			continue // skip IPv6
		}
		elements = append(elements, nftables.SetElement{
			Key:     ip4,
			Timeout: ttl,
		})
	}

	if len(elements) == 0 {
		return nil
	}

	if err := fw.conn.SetAddElements(afw.ipSet, elements); err != nil {
		return fmt.Errorf("adding elements to set: %w", err)
	}
	if err := fw.conn.Flush(); err != nil {
		return fmt.Errorf("flushing set elements: %w", err)
	}

	fw.logger.Debug("added IPs to nftables set",
		"session", sessionPrefix,
		"count", len(elements),
		"ttl", ttl,
	)
	return nil
}

// teardownAgent removes nftables chain, set, and jump rule for an agent.
func (fw *firewall) teardownAgent(sessionPrefix string) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	afw, ok := fw.agents[sessionPrefix]
	if !ok {
		return nil
	}

	// Delete the jump rule from the forward chain.
	// We need to fetch rules from the kernel to get the handle (not available
	// from AddRule until after Flush, and the handle isn't backfilled).
	fw.deleteJumpRule(afw.chain.Name)

	// Flush and delete per-agent chain.
	fw.conn.FlushChain(afw.chain)
	fw.conn.DelChain(afw.chain)
	if err := fw.conn.Flush(); err != nil {
		fw.logger.Warn("flushing chain teardown", "error", err, "session", sessionPrefix)
	}

	// Flush and delete IP set (separate flush — set deletion can conflict with chain rules).
	fw.conn.FlushSet(afw.ipSet)
	fw.conn.DelSet(afw.ipSet)
	if err := fw.conn.Flush(); err != nil {
		fw.logger.Warn("flushing set teardown", "error", err, "session", sessionPrefix)
	}

	delete(fw.agents, sessionPrefix)
	return nil
}

// deleteJumpRule finds and deletes the jump rule targeting chainName from the forward chain.
func (fw *firewall) deleteJumpRule(chainName string) {
	rules, err := fw.conn.GetRules(fw.table, fw.fwdCh)
	if err != nil {
		fw.logger.Warn("listing forward chain rules", "error", err)
		return
	}
	for _, rule := range rules {
		for _, e := range rule.Exprs {
			if v, ok := e.(*expr.Verdict); ok && v.Kind == expr.VerdictJump && v.Chain == chainName {
				if err := fw.conn.DelRule(rule); err != nil {
					fw.logger.Warn("deleting jump rule", "error", err, "chain", chainName)
				} else {
					if err := fw.conn.Flush(); err != nil {
						fw.logger.Warn("flushing jump rule deletion", "error", err)
					}
				}
				return
			}
		}
	}
}

// close removes the entire nftables table.
func (fw *firewall) close() {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	fw.conn.DelTable(fw.table)
	_ = fw.conn.Flush()
}

func policyPtr(p nftables.ChainPolicy) *nftables.ChainPolicy {
	return &p
}
