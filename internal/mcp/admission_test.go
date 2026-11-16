package mcp

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"psi/internal/tools"
)

func TestQueuedCallReportsCancellationOrigin(t *testing.T) {
	for _, phase := range []string{"server", "manager"} {
		for _, origin := range []string{"caller", "deadline", "idle", "connection"} {
			t.Run(phase+"/"+origin, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					manager := NewManager(Options{})
					defer manager.cancel()
					connCtx, cancelConn := context.WithCancel(manager.ctx)
					defer cancelConn()
					tool := Tool{Name: "mcp__fixture__echo", RawName: "echo", Server: "fixture", Epoch: 1, Exposure: ExposureDirect}
					conn := &connection{manager: manager, server: "fixture", epoch: 1, ctx: connCtx, cancel: cancelConn, catalog: []Tool{tool}}
					// An unbuffered semaphore holds the call at the chosen admission
					// stage. No SDK session is needed: dispatch must never happen.
					conn.permits = make(chan struct{})
					if phase == "manager" {
						conn.permits = make(chan struct{}, 1)
						manager.callSlot = make(chan struct{})
					}
					timeout := time.Hour
					if origin == "idle" {
						timeout = time.Second
					}
					manager.servers["fixture"] = &serverState{
						cfg:    Server{Name: "fixture", Enabled: true, Timeout: timeout},
						status: serverStateReady, epoch: 1, conn: conn,
					}
					parent, cancel := context.WithTimeout(context.Background(), time.Minute)
					defer cancel()
					done := make(chan tools.Result, 1)
					go func() { done <- manager.Call(parent, tool, tools.Invocation{Name: tool.Name}) }()
					synctest.Wait()
					select {
					case result := <-done:
						t.Fatalf("call bypassed the admission gate: %+v", result)
					default:
					}
					want := "timed out (admission)"
					switch origin {
					case "caller":
						cancel()
						want = "was cancelled"
					case "deadline":
						time.Sleep(time.Minute) // synctest advances fake time, not wall time.
					case "idle":
						time.Sleep(timeout)
					case "connection":
						cancelConn()
						want = "is unavailable (stale)"
					}
					synctest.Wait()
					select {
					case result := <-done:
						if !result.IsError || len(result.Content) != 1 || result.Content[0].Text != "MCP tool fixture/echo "+want {
							t.Fatalf("queued call = %+v; want %q", result, want)
						}
					default:
						t.Fatal("call remained queued after cancellation")
					}
					if len(conn.permits) != 0 || len(manager.callSlot) != 0 {
						t.Fatal("cancelled call retained admission capacity")
					}
				})
			})
		}
	}
}
