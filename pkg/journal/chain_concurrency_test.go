package journal

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func chainFixture(id string, chain uint64) OnchainIntent {
	return OnchainIntent{ID: id, Kind: "send", ChainID: chain, Sender: "0x1111111111111111111111111111111111111111", Recipient: "0x2222222222222222222222222222222222222222", Token: "native", Amount: "1", Templates: []OnchainTemplate{{ChainID: chain, To: "0x2222222222222222222222222222222222222222", Value: "0x1"}}}
}
func TestOnchainGateByChainAndBridgeCompletion(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	j, err := Open("first")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	a := chainFixture("chain-a", 1)
	a.Kind = "bridge"
	a.OutChain = 8453
	b := chainFixture("chain-b", 10)
	c := chainFixture("chain-c", 137)
	same := chainFixture("chain-a-second", 1)
	for _, in := range []OnchainIntent{a, b, c, same} {
		if _, err = j.CreateOnchain(in); err != nil {
			t.Fatal(err)
		}
	}
	if err = j.BeginOnchain(a.ID, a.Digest(), a.Sender); err != nil {
		t.Fatal(err)
	}
	if err = j.BeginOnchain(b.ID, b.Digest(), b.Sender); err != nil {
		t.Fatal(err)
	}
	if err = j.BeginOnchain(same.ID, same.Digest(), same.Sender); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("same chain must block: %v", err)
	}
	if err = j.AdvanceOnchain(a.ID, "sending", "unknown", ""); err != nil {
		t.Fatal(err)
	}
	if err = j.BeginOnchain(c.ID, c.Digest(), c.Sender); err != nil {
		t.Fatalf("unknown source blocked another chain: %v", err)
	}
	if err = j.BeginOnchain(same.ID, same.Digest(), same.Sender); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("unknown must block source chain: %v", err)
	}
	if err = j.AdvanceOnchain(a.ID, "unknown", "confirmed", ""); err != nil {
		t.Fatal(err)
	}
	if err = j.BeginOnchain(same.ID, same.Digest(), same.Sender); err != nil {
		t.Fatalf("confirmed source must release chain: %v", err)
	}
}
func TestConcurrentHandlesAndChainLocks(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	j, err := Open("first")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	unlock, err := j.LockChain("0xAgent", 1)
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open("first")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	done := make(chan error, 1)
	go func() {
		release, e := other.LockChain("0xagent", 1)
		if e == nil {
			release()
		}
		done <- e
	}()
	select {
	case <-done:
		t.Fatal("same chain lock did not wait")
	case <-time.After(50 * time.Millisecond):
	}
	release, err := other.LockChain("0xagent", 10)
	if err != nil {
		t.Fatalf("different chain blocked: %v", err)
	}
	release()
	unlock()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lock did not release")
	}
	var wg sync.WaitGroup
	fail := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h, e := Open("first")
			if e != nil {
				fail <- e
				return
			}
			defer h.Close()
			in := chainFixture(fmt.Sprintf("concurrent-%d", i), uint64(i+100))
			if _, e = h.CreateOnchain(in); e == nil {
				e = h.BeginOnchain(in.ID, in.Digest(), in.Sender)
			}
			if e == nil {
				e = h.AdvanceOnchain(in.ID, "sending", "confirmed", "")
			}
			if e != nil {
				fail <- e
			}
		}(i)
	}
	wg.Wait()
	close(fail)
	for e := range fail {
		t.Error(e)
	}
}
func TestClaimParentUniqueAcrossHandles(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	j, e := Open("first")
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	a := chainFixture("claim-first", 10)
	a.Kind = "claim"
	a.ParentIntent = "bridge-parent"
	b := a
	b.ID = "claim-second"
	if _, e = j.CreateOnchain(a); e != nil {
		t.Fatal(e)
	}
	if _, e = j.CreateOnchain(b); e == nil {
		t.Fatal("duplicate claim accepted")
	}
	found, e := j.ClaimForBridge("bridge-parent")
	if e != nil || found.ID != a.ID {
		t.Fatalf("claim lookup: %+v %v", found, e)
	}
}
