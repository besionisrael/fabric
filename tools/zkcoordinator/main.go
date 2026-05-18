// HTTP server for the ZooKeeper-coordinated baseline (Paper 3, §III.B).
// Exposes Reserve / Confirm / Cancel / Release / Status endpoints so that
// Caliper workload scripts can drive the M_D exogenous architecture without
// modifying the Fabric peer or orderer.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := os.Getenv("ZK_COORDINATOR_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	c := newCoordinator()

	// Background goroutine: expire stale reservations every 10 s.
	// Prevents coordinator state drift caused by clients that crash after
	// Reserve but before Confirm/Cancel (the structural failure mode the
	// endogenous orderer eliminates).
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			c.expireStaleReservations()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/reserve", makeReserveHandler(c))
	mux.HandleFunc("/v1/confirm", makeConfirmHandler(c))
	mux.HandleFunc("/v1/cancel", makeCancelHandler(c))
	mux.HandleFunc("/v1/release", makeReleaseHandler(c))
	mux.HandleFunc("/v1/status", makeStatusHandler(c))
	mux.HandleFunc("/healthz", healthz)

	log.Printf("zkcoordinator listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// makeReserveHandler handles POST /v1/reserve.
// Body: ReserveRequest JSON.  Response: ReserveResponse JSON.
func makeReserveHandler(c *Coordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req ReserveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		resp := c.Reserve(req)
		writeJSON(w, http.StatusOK, resp)
	}
}

// makeConfirmHandler handles POST /v1/confirm.
// Body: ConfirmRequest JSON (reservationId).
func makeConfirmHandler(c *Coordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req ConfirmRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := c.Confirm(req.ReservationID); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "confirmed"})
	}
}

// makeCancelHandler handles POST /v1/cancel.
// Body: ConfirmRequest JSON (reservationId).
func makeCancelHandler(c *Coordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req ConfirmRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := c.Cancel(req.ReservationID); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
	}
}

// ReleaseRequest is the JSON body for POST /v1/release.
type ReleaseRequest struct {
	Group string `json:"group"`
	Agent string `json:"agent"`
}

// makeReleaseHandler handles POST /v1/release.
// Called by clients after a Transfer or Revoke transaction commits on Fabric,
// decrementing the former holder's confirmed count.
func makeReleaseHandler(c *Coordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req ReleaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := c.Release(req.Group, req.Agent); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
	}
}

// makeStatusHandler handles GET /v1/status?group=<group>.
// Returns a snapshot of group state for observability and debugging.
func makeStatusHandler(c *Coordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		group := r.URL.Query().Get("group")
		if group == "" {
			http.Error(w, "missing query param: group", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, c.GroupStatus(group))
	}
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON encode error: %v", err)
	}
}
