package gateway

import (
	"sync"
	"testing"
	"time"
)

func TestSessionLocksSerializeSameSession(t *testing.T) {
	locks := newSessionLocks()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		unlock := locks.Lock("feishu:thread")
		defer unlock()
		close(firstStarted)
		<-releaseFirst
	}()
	go func() {
		defer wg.Done()
		<-firstStarted
		unlock := locks.Lock("feishu:thread")
		defer unlock()
		close(secondEntered)
	}()
	select {
	case <-secondEntered:
		t.Fatalf("expected same session lock to block")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatalf("expected second lock to enter after first releases")
	}
	wg.Wait()
}

func TestSessionLocksAllowDifferentSessions(t *testing.T) {
	locks := newSessionLocks()
	unlock := locks.Lock("feishu:a")
	defer unlock()
	entered := make(chan struct{})
	go func() {
		unlockB := locks.Lock("feishu:b")
		defer unlockB()
		close(entered)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatalf("expected different session lock to enter immediately")
	}
}
