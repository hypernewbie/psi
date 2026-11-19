package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscoveryNotificationSurvivesConnectionPublication(t *testing.T) {
	pingReplies := make(chan struct{}, 1)
	var lists atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch request.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", "setup-notification-fixture")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"fixture","version":"1"}}}`, request.ID)
		case "tools/list":
			list := lists.Add(1)
			name := "before"
			if list > 1 {
				name = "after"
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"notification-barrier\",\"method\":\"ping\"}\n\n")
			w.(http.Flusher).Flush()
			// SDK request handlers are ordered. A ping reply proves the earlier
			// notification was handled before discovery returns its stale list.
			select {
			case <-pingReplies:
			case <-r.Context().Done():
				return
			}
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"tools\":[{\"name\":%q,\"inputSchema\":{\"type\":\"object\"}}]}}\n\n", request.ID, name)
		case "":
			if string(request.ID) != `"notification-barrier"` {
				t.Errorf("unexpected response ID: %s", request.ID)
			}
			w.WriteHeader(http.StatusAccepted)
			pingReplies <- struct{}{}
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(server.Close)
	fixedNow := time.Now()
	sleeps := 0
	manager := NewManager(Options{
		now: func() time.Time { return fixedNow },
		sleep: func(context.Context, time.Duration) bool {
			sleeps++
			// The second list response sends another real notification. Stop at
			// its rate-limit wait, after the first refresh has been published.
			return sleeps == 1
		},
	})
	t.Cleanup(func() { manager.Stop(); manager.Wait() })
	cfg := Server{Name: "fixture", Transport: TransportHTTP, URL: server.URL, Enabled: true, Exposure: ExposureDirect, Timeout: time.Minute}
	state := &serverState{cfg: cfg, epoch: 1}
	manager.servers[cfg.Name] = state
	manager.order = []string{cfg.Name}
	_, conn, err := manager.connect(t.Context(), state, state.epoch, cfg, Input{})
	if err != nil {
		t.Fatal(err)
	}
	state.conn, state.catalog, state.status = conn, conn.catalog, serverStateReady
	manager.publishLocked()
	before := manager.Snapshot()
	if _, ok := before.Find("mcp__fixture__before"); !ok {
		t.Fatal("initial catalog missing")
	}
	select {
	case <-conn.listCh:
		conn.signalListChanged()
	default:
		t.Fatal("notification received during discovery was discarded before publication")
	}
	conn.runListChanges()
	after := manager.Snapshot()
	if _, ok := after.Find("mcp__fixture__after"); !ok || len(after.Tools) != 1 {
		t.Fatalf("pending discovery notification did not refresh the catalog: %+v", after)
	}
	if _, ok := before.Find("mcp__fixture__before"); !ok {
		t.Fatal("refresh mutated the original snapshot")
	}
	if lists.Load() != 2 || sleeps != 2 {
		t.Fatalf("list requests=%d, rate-limit waits=%d; want 2 each", lists.Load(), sleeps)
	}
}
