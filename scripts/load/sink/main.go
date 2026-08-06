// Command sink is the callback-soak receiver (P6-08). aero-id callback
// subscriptions and deliveries POST here; the sink answers 2xx to every
// request and exposes counters so the k6 callback soak can assert delivery
// progress and signature headers without standing up a real consumer.
//
// It is a load-test tool only — never ship it in a production cell.
//
// Usage:
//
//	go run ./scripts/load/sink -addr :9091
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

type stats struct {
	Verifications int64 `json:"verifications"`
	Deliveries    int64 `json:"deliveries"`
	Signed        int64 `json:"signed_deliveries"`
	Rejected      int64 `json:"rejected"`
}

var (
	deliveries atomic.Int64
	verif      atomic.Int64
	signed     atomic.Int64
	rejected   atomic.Int64
)

func main() {
	addr := flag.String("addr", ":9091", "listen address")
	flag.Parse()
	http.HandleFunc("/", handle)
	http.HandleFunc("/stats", handleStats)
	http.HandleFunc("/reset", handleReset)
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	server := &http.Server{
		Addr:              *addr,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("sink listening on %s", *addr)
	log.Fatal(server.ListenAndServe())
}

func handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	var envelope map[string]any
	_ = json.Unmarshal(body, &envelope)
	if envelope["type"] == "aero.callback.verify" || r.Header.Get("X-Aero-Callback-Challenge") != "" {
		verif.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"verified"}`))
		return
	}
	deliveries.Add(1)
	if r.Header.Get("X-Aero-Callback-Signature") != "" {
		signed.Add(1)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"accepted"}`))
}

func handleStats(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats{
		Verifications: verif.Load(),
		Deliveries:    deliveries.Load(),
		Signed:        signed.Load(),
		Rejected:      rejected.Load(),
	})
}

func handleReset(w http.ResponseWriter, _ *http.Request) {
	verif.Store(0)
	deliveries.Store(0)
	signed.Store(0)
	rejected.Store(0)
	w.WriteHeader(http.StatusNoContent)
}
