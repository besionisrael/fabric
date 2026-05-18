// Package zkcoordinator implements the ZooKeeper-coordinated baseline
// architecture described in Paper 3, §III.B.
//
// # Architecture
//
// This service is the "external coordination service" of the ZK + DLT baseline.
// It evaluates global usage constraints (C_global) before transactions are
// submitted to Fabric, instantiating M_D in an exogenous fashion: constraint
// enforcement and ledger commit occur in separate systems, linked by the
// client-side notification protocol described below.
//
// # Protocol
//
//  1. Client calls Reserve(group, agent, maxConcurrent) before endorsing tx.
//  2. If admitted: coordinator reserves a slot (speculative update) and returns
//     a reservation ID.
//  3. Client endorses and submits the transaction to Fabric.
//  4. After Fabric commit:  client calls Confirm(reservationID).
//  5. After Fabric abort:   client calls Cancel(reservationID).
//
// The correctness of the compliance guarantee depends on the reliability of
// steps 4–5. If a client fails to call Confirm or Cancel, the coordinator's
// state diverges from the ledger, and subsequent evaluations may be incorrect.
// This is the structural failure mode that the constraint-aware orderer (M_D
// endogenous) eliminates by tying evaluation and commit to a single Raft round.
//
// # Implementation note
//
// The state is held in-memory with mutex serialization, which provides the
// same admission semantics as a ZooKeeper-backed deployment (serialized
// decisions, atomic reserve/confirm). A production deployment would replace
// the in-memory maps with ZooKeeper znodes and distributed ephemeral locks,
// ensuring availability under coordinator node failures. For the evaluation
// in Paper 3, the in-memory implementation is sufficient: it is correct,
// benchmarkable, and isolates the architectural difference under study.
package main

import (
	"fmt"
	"sync"
	"time"
)

// GroupState tracks the C_global constraint for one SubsetGroup,
// mirroring the BioankCacheState in the constraint-aware orderer.
type GroupState struct {
	// Confirmed holds agent IDs whose Fabric transactions have been committed.
	Confirmed map[string]int // agent → count of confirmed resources held

	// Reserved holds agent IDs with in-flight reservations (pending Confirm/Cancel).
	Reserved map[string]string // reservationID → agent

	MaxConcurrent int
}

// currentHolderCount returns the number of distinct agents with at least one
// confirmed holding in this group, plus any in-flight reservations for agents
// not already confirmed. This is |S_sub| from Paper 2, Definition 3.
func (g *GroupState) currentHolderCount() int {
	agents := make(map[string]struct{}, len(g.Confirmed))
	for a := range g.Confirmed {
		agents[a] = struct{}{}
	}
	for _, a := range g.Reserved {
		agents[a] = struct{}{}
	}
	return len(agents)
}

// Coordinator is the exogenous M_D enforcer. It serializes all admission
// decisions for C_global through a mutex, ensuring that no two concurrent
// clients can both see the constraint as satisfied when it is not.
type Coordinator struct {
	mu     sync.Mutex
	groups map[string]*GroupState // subsetGroup → GroupState

	// reservations maps reservationID → (group, agent) for the Confirm/Cancel path.
	reservations map[string]*reservation
}

type reservation struct {
	id        string
	group     string
	agent     string
	expiresAt time.Time
}

// ReserveRequest is the JSON body for POST /v1/reserve.
type ReserveRequest struct {
	Group         string `json:"group"`
	Agent         string `json:"agent"`
	MaxConcurrent int    `json:"maxConcurrent"`
}

// ReserveResponse is the JSON response for POST /v1/reserve.
type ReserveResponse struct {
	Admitted      bool   `json:"admitted"`
	ReservationID string `json:"reservationId,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// ConfirmRequest is the JSON body for POST /v1/confirm or POST /v1/cancel.
type ConfirmRequest struct {
	ReservationID string `json:"reservationId"`
}

func newCoordinator() *Coordinator {
	return &Coordinator{
		groups:       make(map[string]*GroupState),
		reservations: make(map[string]*reservation),
	}
}

// Reserve evaluates C_global and, if admissible, creates a speculative
// reservation. The caller must follow up with Confirm or Cancel.
//
// This is the critical section: the mutex ensures that evaluation and
// speculative update are atomic with respect to all other Reserve calls,
// providing the same admission semantics as Algorithm 1 of Paper 2.
func (c *Coordinator) Reserve(req ReserveRequest) ReserveResponse {
	c.mu.Lock()
	defer c.mu.Unlock()

	if req.Group == "" || req.Agent == "" {
		// No group constraint: admit unconditionally.
		return ReserveResponse{Admitted: true}
	}

	gs, ok := c.groups[req.Group]
	if !ok {
		gs = &GroupState{
			Confirmed:     make(map[string]int),
			Reserved:      make(map[string]string),
			MaxConcurrent: req.MaxConcurrent,
		}
		c.groups[req.Group] = gs
	}

	// Update MaxConcurrent if this call provides a tighter bound.
	if req.MaxConcurrent > 0 && gs.MaxConcurrent == 0 {
		gs.MaxConcurrent = req.MaxConcurrent
	}

	if gs.MaxConcurrent == 0 {
		return ReserveResponse{Admitted: true}
	}

	// Check whether adding this agent would exceed K_max.
	// If the agent already holds a confirmed resource in this group,
	// the count does not increase.
	if gs.Confirmed[req.Agent] > 0 {
		return ReserveResponse{Admitted: true}
	}
	// Also check in-flight reservations.
	for _, a := range gs.Reserved {
		if a == req.Agent {
			return ReserveResponse{Admitted: true}
		}
	}

	if gs.currentHolderCount()+1 > gs.MaxConcurrent {
		return ReserveResponse{
			Admitted: false,
			Reason: fmt.Sprintf("C_global: group %q has %d/%d concurrent holders; admitting agent %q would exceed K_max",
				req.Group, gs.currentHolderCount(), gs.MaxConcurrent, req.Agent),
		}
	}

	// Admit: create speculative reservation.
	rid := fmt.Sprintf("%s-%s-%d", req.Group, req.Agent, time.Now().UnixNano())
	r := &reservation{
		id:        rid,
		group:     req.Group,
		agent:     req.Agent,
		expiresAt: time.Now().Add(30 * time.Second),
	}
	gs.Reserved[rid] = req.Agent
	c.reservations[rid] = r

	return ReserveResponse{Admitted: true, ReservationID: rid}
}

// Confirm finalizes a reservation after the Fabric transaction has been
// committed. The speculative slot becomes a confirmed holding.
func (c *Coordinator) Confirm(reservationID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	r, ok := c.reservations[reservationID]
	if !ok {
		return fmt.Errorf("reservation %q not found", reservationID)
	}

	gs := c.groups[r.group]
	delete(gs.Reserved, reservationID)
	gs.Confirmed[r.agent]++
	delete(c.reservations, reservationID)
	return nil
}

// Cancel rolls back a reservation after a Fabric transaction abort or timeout.
func (c *Coordinator) Cancel(reservationID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	r, ok := c.reservations[reservationID]
	if !ok {
		return fmt.Errorf("reservation %q not found", reservationID)
	}

	gs := c.groups[r.group]
	delete(gs.Reserved, reservationID)
	delete(c.reservations, reservationID)
	return nil
}

// Release decrements a confirmed holding (called when a Transfer or Revoke
// removes an agent from a group). The client calls this after the Fabric
// transaction commits.
func (c *Coordinator) Release(group, agent string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	gs, ok := c.groups[group]
	if !ok {
		return nil
	}
	if gs.Confirmed[agent] > 0 {
		gs.Confirmed[agent]--
		if gs.Confirmed[agent] == 0 {
			delete(gs.Confirmed, agent)
		}
	}
	return nil
}

// GroupStatus returns a snapshot of the group state for observability.
func (c *Coordinator) GroupStatus(group string) map[string]interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()

	gs, ok := c.groups[group]
	if !ok {
		return map[string]interface{}{"group": group, "holders": 0}
	}
	return map[string]interface{}{
		"group":         group,
		"confirmed":     gs.Confirmed,
		"inFlight":      len(gs.Reserved),
		"holderCount":   gs.currentHolderCount(),
		"maxConcurrent": gs.MaxConcurrent,
	}
}

// expireStaleReservations removes reservations that have exceeded their
// TTL without a Confirm or Cancel. This prevents coordinator state from
// diverging permanently due to crashed clients.
// Called periodically by the server.
func (c *Coordinator) expireStaleReservations() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for rid, r := range c.reservations {
		if now.After(r.expiresAt) {
			gs := c.groups[r.group]
			if gs != nil {
				delete(gs.Reserved, rid)
			}
			delete(c.reservations, rid)
		}
	}
}
