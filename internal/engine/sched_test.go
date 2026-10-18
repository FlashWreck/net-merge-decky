package engine

import (
	"net"
	"syscall"
	"testing"
	"time"

	"net-merge/internal/nics"
)

func nic(name, ip string, w float64) nics.Nic {
	return nics.Nic{Name: name, IP: net.ParseIP(ip), Weight: w}
}

func countBy(picks []*Slot, name string) int {
	n := 0
	for _, p := range picks {
		if p.Name == name {
			n++
		}
	}
	return n
}

func acquireN(s *Scheduler, n int) []*Slot {
	out := make([]*Slot, n)
	for i := range out {
		out[i] = s.Acquire(nil)
	}
	return out
}

func TestEqualWeightsSpreadEvenly(t *testing.T) {
	s := NewScheduler([]nics.Nic{nic("A", "10.0.0.1", 1), nic("B", "10.0.0.2", 1)})
	p := acquireN(s, 6)
	if countBy(p, "A") != 3 || countBy(p, "B") != 3 {
		t.Fatalf("want 3/3, got A=%d B=%d", countBy(p, "A"), countBy(p, "B"))
	}
}

func TestWeightsSkew(t *testing.T) {
	s := NewScheduler([]nics.Nic{nic("A", "10.0.0.1", 3), nic("B", "10.0.0.2", 1)})
	p := acquireN(s, 8)
	if countBy(p, "A") != 6 || countBy(p, "B") != 2 {
		t.Fatalf("want 6/2, got A=%d B=%d", countBy(p, "A"), countBy(p, "B"))
	}
}

func TestReleaseFreesNic(t *testing.T) {
	s := NewScheduler([]nics.Nic{nic("A", "10.0.0.1", 1), nic("B", "10.0.0.2", 1)})
	first := s.Acquire(nil)
	s.Acquire(nil)
	s.Release(first)
	if got := s.Acquire(nil); got != first {
		t.Fatalf("want the released NIC %s, got %s", first.Name, got.Name)
	}
}

func TestFailCooldown(t *testing.T) {
	s := NewScheduler([]nics.Nic{nic("A", "10.0.0.1", 1), nic("B", "10.0.0.2", 1)})
	clock := time.Unix(1000, 0)
	s.now = func() time.Time { return clock }
	bad := s.Snapshot()[0]
	var badSlot *Slot
	for _, sl := range s.slots {
		if sl.Name == bad.Name {
			badSlot = sl
		}
	}
	s.Fail(badSlot, Fam4, 5*time.Second)
	for i := 0; i < 4; i++ {
		if s.Acquire(nil) == badSlot {
			t.Fatal("cooling-down NIC was picked")
		}
	}
	clock = clock.Add(6 * time.Second)
	seen := false
	for i := 0; i < 6; i++ {
		if s.Acquire(nil) == badSlot {
			seen = true
		}
	}
	if !seen {
		t.Fatal("NIC never came back after cooldown")
	}
}

func TestAllDownStillServes(t *testing.T) {
	s := NewScheduler([]nics.Nic{nic("A", "10.0.0.1", 1)})
	s.Fail(s.slots[0], Fam4, time.Hour)
	if s.Acquire(nil) == nil {
		t.Fatal("total outage: all-NICs-down must still hand one out")
	}
}

func TestProbeStateTiers(t *testing.T) {
	s := NewScheduler([]nics.Nic{nic("A", "10.0.0.1", 1), nic("B", "10.0.0.2", 1), nic("C", "10.0.0.3", 1)})
	a, b, c := s.slots[0], s.slots[1], s.slots[2]
	s.SetState(a, Fam4, true)
	s.SetState(c, Fam4, false)
	// best tier gets all flows
	for i := 0; i < 5; i++ {
		if got := s.Acquire(nil); got != a {
			t.Fatalf("flow %d went to %s, want the probed-up NIC", i, got.Name)
		}
	}
	s.SetState(a, Fam4, false) // unprobed preferred over dead
	if got := s.Acquire(nil); got != b {
		t.Fatalf("want unprobed NIC over dead ones, got %s", got.Name)
	}
	s.SetState(b, Fam4, false) // fallback when all links fail
	if s.Acquire(nil) == nil {
		t.Fatal("all-dead must still hand out a NIC")
	}
	s.SetState(c, Fam4, true) // recovered link takes over
	if got := s.Acquire(nil); got != c {
		t.Fatalf("recovered NIC should win, got %s", got.Name)
	}
}

func TestProbeAgainstLocalListener(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	old := probeTargets[Fam4]
	defer func() { probeTargets[Fam4] = old }()
	probeTargets[Fam4] = []string{ln.Addr().String()}

	s := NewScheduler([]nics.Nic{nic("A", "127.0.0.2", 1)})
	if !probe(s.slots[0], Fam4) {
		t.Fatal("probe should pass against a reachable target")
	}
	ln.Close()
	if probe(s.slots[0], Fam4) {
		t.Fatal("probe should fail once the target is gone")
	}
}

func TestIPv6OnlyGoesToNicsWithV6(t *testing.T) {
	n6 := nic("B", "10.0.0.2", 1)
	n6.IP6 = net.ParseIP("2001:db8::2")
	s := NewScheduler([]nics.Nic{nic("A", "10.0.0.1", 1), n6})
	for i := 0; i < 4; i++ {
		if got := s.AcquireFor(Fam6, nil); got == nil || got.Name != "B" {
			t.Fatalf("v6 flow %d went to %v, want the only v6-capable NIC B", i, got)
		}
	}
	if got := s.AcquireFor(Fam4, nil); got == nil {
		t.Fatal("v4 flows must still work")
	}
	s2 := NewScheduler([]nics.Nic{nic("A", "10.0.0.1", 1)})
	if s2.AcquireFor(Fam6, nil) != nil {
		t.Fatal("no v6-capable NIC: v6 flows must be refused so apps fall back to IPv4")
	}
}

func TestV6StateIsIndependentOfV4(t *testing.T) {
	n := nic("A", "10.0.0.1", 1)
	n.IP6 = net.ParseIP("2001:db8::1")
	m := nic("B", "10.0.0.2", 1)
	m.IP6 = net.ParseIP("2001:db8::2")
	s := NewScheduler([]nics.Nic{n, m})
	a, b := s.slots[0], s.slots[1]
	s.SetState(a, Fam4, true)
	s.SetState(b, Fam4, true)
	s.SetState(a, Fam6, false) // v4 working but v6 failed
	s.SetState(b, Fam6, true)
	for i := 0; i < 4; i++ {
		got := s.AcquireFor(Fam6, nil)
		if got != b {
			t.Fatalf("v6 flow %d went to %s, want B", i, got.Name)
		}
		s.ReleaseFor(got, Fam6)
	}
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		seen[s.AcquireFor(Fam4, nil).Name] = true
	}
	if !seen["A"] || !seen["B"] {
		t.Fatalf("v4 should still use both NICs, got %v", seen)
	}
}

// auto weights calculated per family
func TestAutoWeightsArePerFamily(t *testing.T) {
	a := nic("A", "10.0.0.1", 1)
	a.IP6 = net.ParseIP("2001:db8::1")
	b := nic("B", "10.0.0.2", 1)
	b.IP6 = net.ParseIP("2001:db8::2")
	s := NewScheduler([]nics.Nic{a, b})
	s.SetAuto(true)
	sa, sb := s.slots[0], s.slots[1]
	const mbit = 125_000 // bytes per sec
	// simulate 100 mbit vs 5 mbit
	for i := 0; i < 3; i++ {
		sa.bytesIn[Fam4].Add(100 * mbit)
		sb.bytesIn[Fam4].Add(5 * mbit)
		sa.bytesIn[Fam6].Add(100 * mbit)
		sb.bytesIn[Fam6].Add(100 * mbit)
		s.Tick(time.Second)
	}
	if got := sb.weight[Fam4] / sa.weight[Fam4]; got > 0.06 {
		t.Fatalf("slow v4 path should be down-weighted to ~0.05, got ratio %.2f", got)
	}
	if got := sb.weight[Fam6] / sa.weight[Fam6]; got < 0.95 {
		t.Fatalf("fast v6 path must keep full weight, got ratio %.2f", got)
	}
	// peak capacity retained while idle
	for i := 0; i < 20; i++ {
		s.Tick(time.Second)
	}
	if sb.peak[Fam6] < 90*mbit {
		t.Fatalf("idle NIC forgot its capacity: peak %.0f", sb.peak[Fam6])
	}
}

// weights recover after temporary drop
func TestPessimisticEstimateRecoversWhileIdle(t *testing.T) {
	s := NewScheduler([]nics.Nic{nic("A", "10.0.0.1", 1), nic("B", "10.0.0.2", 1)})
	s.SetAuto(true)
	sa, sb := s.slots[0], s.slots[1]
	const mbit = 125_000
	for i := 0; i < 3; i++ { // simulate slow link
		sa.bytesIn[Fam4].Add(100 * mbit)
		sb.bytesIn[Fam4].Add(5 * mbit)
		s.Tick(time.Second)
	}
	if w := sb.weight[Fam4] / sa.weight[Fam4]; w > 0.06 {
		t.Fatalf("setup: B should be down-weighted, ratio %.2f", w)
	}
	for i := 0; i < 80; i++ { // idle link recovery
		sa.bytesIn[Fam4].Add(100 * mbit)
		s.Tick(time.Second)
	}
	if w := sb.weight[Fam4] / sa.weight[Fam4]; w < 0.45 {
		t.Fatalf("idle NIC should drift back up to ~0.5 so it is re-measured, ratio %.2f", w)
	}
}

func TestV6CoverageDecidesWhetherIPv6IsBalanced(t *testing.T) {
	a := nic("A", "10.0.0.1", 1)
	a.IP6 = net.ParseIP("2001:db8::1")
	b := nic("B", "10.0.0.2", 1) // ipv4 only
	s := NewScheduler([]nics.Nic{a, b})
	if s.V6CoversAll() {
		t.Fatal("B has no IPv6: IPv6 would funnel onto A, coverage must be partial")
	}
	if !s.V6Usable() {
		t.Fatal("A can carry IPv6")
	}
	b.IP6 = net.ParseIP("2001:db8::2")
	s.Update([]nics.Nic{a, b})
	if !s.V6CoversAll() {
		t.Fatal("every adapter has IPv6 now")
	}
	s.SetState(s.slots[1], Fam6, false) // v6 probe failed
	if s.V6CoversAll() {
		t.Fatal("a dead IPv6 path on B means partial coverage again")
	}
	only4 := NewScheduler([]nics.Nic{nic("A", "10.0.0.1", 1)})
	if only4.V6CoversAll() || only4.V6Usable() {
		t.Fatal("no IPv6 anywhere: nothing to allow")
	}
}

func TestSkipSet(t *testing.T) {
	s := NewScheduler([]nics.Nic{nic("A", "10.0.0.1", 1), nic("B", "10.0.0.2", 1)})
	a, b := s.slots[0], s.slots[1]
	if got := s.Acquire(map[string]struct{}{a.key: {}}); got != b {
		t.Fatal("skip set not honoured")
	}
	if got := s.Acquire(map[string]struct{}{a.key: {}, b.key: {}}); got != nil {
		t.Fatal("want nil when everything is skipped")
	}
}

func TestUpdateAddsAndKeepsCounters(t *testing.T) {
	s := NewScheduler([]nics.Nic{nic("A", "10.0.0.1", 1)})
	s.Acquire(nil)
	s.Update([]nics.Nic{nic("A", "10.0.0.1", 1), nic("B", "10.0.0.2", 1)})
	snap := s.Snapshot()
	if len(snap) != 2 || snap[0].Total != 1 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}

func TestUpdateDropsVanishedWhenIdle(t *testing.T) {
	s := NewScheduler([]nics.Nic{nic("A", "10.0.0.1", 1), nic("B", "10.0.0.2", 1)})
	busy := s.Acquire(nil)
	s.Update(nil)
	if len(s.Snapshot()) != 1 {
		t.Fatalf("idle NIC should be dropped, busy one kept: %+v", s.Snapshot())
	}
	if s.Acquire(nil) != nil {
		t.Fatal("vanished NIC must not get new flows")
	}
	s.Release(busy)
	s.Update(nil)
	if len(s.Snapshot()) != 0 {
		t.Fatal("NIC should be dropped once idle")
	}
}

func TestReplugWithNewIndexStartsFresh(t *testing.T) {
	a := nic("A", "10.0.0.1", 1)
	a.Index = 5
	s := NewScheduler([]nics.Nic{a})
	old := s.Acquire(nil)
	s.SetState(old, Fam4, false)
	s.Fail(old, Fam4, time.Hour)

	// device vanishes while a flow is still open, then returns with a new index
	s.Update(nil)
	a.Index = 9
	s.Update([]nics.Nic{a})

	got := s.Acquire(nil)
	if got == nil || got == old {
		t.Fatalf("want a fresh slot, got %v", got)
	}
	if got.Index != 9 {
		t.Fatalf("want new index 9, got %d", got.Index)
	}
	if got.state[Fam4] != stateUnknown || !got.downUntil[Fam4].IsZero() {
		t.Fatal("fresh slot must not inherit down state or cooldown")
	}
	if len(s.Snapshot()) != 1 {
		t.Fatalf("old slot should be replaced, got %+v", s.Snapshot())
	}
	s.Release(old) // late release on the discarded slot must be harmless
}

func TestReplugSameIndexReverifies(t *testing.T) {
	a := nic("A", "10.0.0.1", 1)
	a.Index = 5
	s := NewScheduler([]nics.Nic{a})
	busy := s.Acquire(nil)
	s.SetState(busy, Fam4, false)
	s.Fail(busy, Fam4, time.Hour)
	s.Update(nil)
	s.Update([]nics.Nic{a})

	got := s.Acquire(nil)
	if got != busy {
		t.Fatal("same device should keep its slot and live flows")
	}
	if got.state[Fam4] != stateUnknown || !got.downUntil[Fam4].IsZero() || !got.lastProbe[Fam4].IsZero() {
		t.Fatal("reconnect must clear down state, cooldown and probe timer")
	}
}

func TestStillPresentKeepsState(t *testing.T) {
	a := nic("A", "10.0.0.1", 1)
	a.Index = 5
	s := NewScheduler([]nics.Nic{a})
	sl := s.Acquire(nil)
	s.SetState(sl, Fam4, false)
	s.Update([]nics.Nic{a})
	s.Update([]nics.Nic{a})
	if sl.state[Fam4] != stateDown {
		t.Fatal("rescans of a present adapter must not reset its state")
	}
}

func TestClassifyMissingDeviceIsLinkDown(t *testing.T) {
	c := classify(&net.OpError{Op: "dial", Err: syscall.ENODEV})
	if !c.linkDown || !c.retry {
		t.Fatalf("ENODEV should mark link down and retry, got %+v", c)
	}
}
