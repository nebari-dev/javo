package local_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/auth/local"
)

// watchHashes replaces argon2id with a stub that takes d and records how
// many run at once; release lets blocked ones finish.
func watchHashes(t *testing.T, d time.Duration, block chan struct{}) *atomic.Int32 {
	t.Helper()
	var running, peak atomic.Int32
	old := *local.IDKey
	*local.IDKey = func(pw, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
		n := running.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		if block != nil {
			<-block
		}
		sleep(d)
		running.Add(-1)
		return make([]byte, keyLen)
	}
	t.Cleanup(func() { *local.IDKey = old })
	return &peak
}

func sleep(d time.Duration) { <-time.After(d) }

// Sign-ins run at most local.MaxConcurrentHashes argon2id hashes at once,
// each about 19 MiB, so a burst of attempts cannot run the gateway out of
// memory; the rest wait their turn.
func TestOnlyAFewHashesRunAtOnce(t *testing.T) {
	users, _ := newUsers(t)
	peak := watchHashes(t, 20*time.Millisecond, nil)
	var wg sync.WaitGroup
	for range 6 * local.MaxConcurrentHashes {
		wg.Go(func() {
			if _, err := users.Verify(context.Background(), "nobody", "guess"); !errors.Is(err, local.ErrInvalidCredentials) {
				t.Errorf("verify: %v", err)
			}
		})
	}
	wg.Wait()
	if got := peak.Load(); got > local.MaxConcurrentHashes {
		t.Fatalf("%d hashes ran at once, more than %d", got, local.MaxConcurrentHashes)
	}
}

// When every slot stays busy past the wait, a sign-in is refused at once
// with ErrBusy instead of queueing without bound.
func TestASignInIsRefusedWhenHashingStaysBusy(t *testing.T) {
	users, _ := newUsers(t)
	block := make(chan struct{})
	watchHashes(t, 0, block)
	old := *local.HashWait
	*local.HashWait = 50 * time.Millisecond
	t.Cleanup(func() { *local.HashWait = old })
	var wg sync.WaitGroup
	for range local.MaxConcurrentHashes {
		wg.Go(func() { _, _ = users.Verify(context.Background(), "nobody", "guess") })
	}
	defer func() { close(block); wg.Wait() }()
	sleep(20 * time.Millisecond) // the first ones hold every slot
	got := make(chan error, 1)
	go func() {
		_, err := users.Verify(context.Background(), "nobody", "guess")
		got <- err
	}()
	select {
	case err := <-got:
		if !errors.Is(err, local.ErrBusy) {
			t.Fatalf("verify while busy: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a sign-in waited on busy hashing instead of being refused")
	}
}
