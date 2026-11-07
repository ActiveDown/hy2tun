//go:build windows

package main

import (
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func TestServiceStartupCheckpoints(t *testing.T) {
	statuses := make(chan svc.Status, 10)
	finish := reportServiceStartup(statuses)
	defer finish()
	first := <-statuses
	if first.State != svc.StartPending || first.CheckPoint == 0 || first.WaitHint == 0 {
		t.Fatalf("SCM needs a startup checkpoint and wait hint: %+v", first)
	}
	select {
	case next := <-statuses:
		if next.State != svc.StartPending || next.CheckPoint <= first.CheckPoint {
			t.Fatalf("startup stalled: %+v", next)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("no startup heartbeat")
	}
	finish()
	finish() // cleanup is safe on both success and early return
	// No START_PENDING can be emitted after the service reports RUNNING.
	for len(statuses) > 0 {
		<-statuses
	}
	time.Sleep(50 * time.Millisecond)
	if len(statuses) != 0 {
		t.Fatal("startup reporter survived RUNNING")
	}
}
