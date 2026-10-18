package engine

import (
	"math"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"net-merge/internal/nics"
)

// family selects address family for flow pinning
type Family int

const (
	Fam4 Family = iota
	Fam6
)

// link state from probes
type linkState int

const (
	stateUnknown linkState = iota
	stateUp
	stateDown
)

func (l linkState) String() string {
	return [...]string{"checking", "up", "no internet"}[l]
}

type slotAddr struct{ ip4, ip6 net.IP }

// slot wraps adapter with atomic traffic counters
type Slot struct {
	Name  string
	Index int
	key   string
	addr  atomic.Pointer[slotAddr]

	active, total, failed atomic.Int64 // active across families
	actF                  [2]atomic.Int64
	bytesIn, bytesOut     [2]atomic.Int64
	probing               [2]atomic.Bool

	// guarded by mu
	weight          [2]float64
	present         bool
	gone            bool // missing at the previous rescan
	state           [2]linkState
	lastProbe       [2]time.Time
	downUntil       [2]time.Time
	lastIn, lastOut [2]int64
	rateIn, rateOut [2]float64 // bytes per sec
	peak            [2]float64 // observed peak capacity

	// total link traffic from os counters
	linkIn, linkOut         uint64
	linkRateIn, linkRateOut float64
	linkPrimed              bool
}

func (sl *Slot) ip(f Family) net.IP {
	a := sl.addr.Load()
	if f == Fam6 {
		return a.ip6
	}
	return a.ip4
}

type SlotStats struct {
	Name              string  `json:"name"`
	IP                string  `json:"ip"`
	IP6               string  `json:"ip6"`
	State             string  `json:"state"` // ipv4 probe state
	State6            string  `json:"state6"`
	Weight            float64 `json:"weight"`
	Weight6           float64 `json:"weight6"`
	Present           bool    `json:"present"`
	Active            int64   `json:"active"`
	Total             int64   `json:"total"`
	Failed            int64   `json:"failed"`
	BytesIn           int64   `json:"bytes_in"`
	BytesOut          int64   `json:"bytes_out"`
	RateIn            float64 `json:"rate_in"` // aggregated throughput
	RateOut           float64 `json:"rate_out"`
	LinkIn            float64 `json:"link_in"` // os interface traffic
	LinkOut           float64 `json:"link_out"`
	Peak              float64 `json:"peak"`
	Peak6             float64 `json:"peak6"`
}

// scheduler assigns new flows using weighted least active flows
type Scheduler struct {
	mu    sync.Mutex
	slots []*Slot
	auto  bool
	now   func() time.Time
}

func newSlot(n nics.Nic, a *slotAddr) *Slot {
	sl := &Slot{Name: n.Name, Index: n.Index, key: n.Name, weight: [2]float64{n.Weight, n.Weight}, present: true}
	sl.addr.Store(a)
	return sl
}

func NewScheduler(nics []nics.Nic) *Scheduler {
	s := &Scheduler{now: time.Now}
	s.Update(nics)
	return s
}

// set auto weight derivation from throughput
func (s *Scheduler) SetAuto(on bool) {
	s.mu.Lock()
	s.auto = on
	s.mu.Unlock()
}

func normAddr(n nics.Nic) *slotAddr {
	a := &slotAddr{}
	if n.IP != nil {
		a.ip4 = n.IP.To4()
	}
	if n.IP6 != nil && n.IP6.To4() == nil {
		a.ip6 = n.IP6
	}
	return a
}

// update applies rescan and preserves counters
func (s *Scheduler) Update(nics []nics.Nic) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sl := range s.slots {
		sl.gone = !sl.present
		sl.present = false
	}
	for _, n := range nics {
		na := normAddr(n)
		var found *Slot
		pos := -1
		for i, sl := range s.slots {
			if sl.key == n.Name {
				found, pos = sl, i
				break
			}
		}
		if found == nil {
			s.slots = append(s.slots, newSlot(n, na))
			continue
		}
		if found.Index != n.Index {
			// device was recreated, so old binds are dead and the slot starts fresh
			s.slots[pos] = newSlot(n, na)
			continue
		}
		if found.gone {
			// same device came back, verify it again right away
			found.state = [2]linkState{}
			found.downUntil = [2]time.Time{}
			found.lastProbe = [2]time.Time{}
		}
		found.present = true
		if !s.auto {
			found.weight = [2]float64{n.Weight, n.Weight}
		}
		old := found.addr.Load()
		if !old.ip4.Equal(na.ip4) {
			found.state[Fam4] = stateUnknown // reverify on address change
		}
		if !old.ip6.Equal(na.ip6) {
			found.state[Fam6] = stateUnknown
		}
		if !old.ip4.Equal(na.ip4) || !old.ip6.Equal(na.ip6) {
			found.addr.Store(na)
		}
	}
	kept := s.slots[:0]
	for _, sl := range s.slots {
		if sl.present || sl.active.Load() > 0 {
			kept = append(kept, sl)
		}
	}
	s.slots = kept
}

// min throughput threshold for measurement
const minMeasuredPeak = 50_000

// tick samples throughput per family and adjusts weights
func (s *Scheduler) Tick(dt time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := dt.Seconds()
	for _, sl := range s.slots {
		in, out, ok := nics.Octets(sl.Index)
		if !sl.present || !ok {
			sl.linkRateIn, sl.linkRateOut, sl.linkPrimed = 0, 0, false
			continue
		}
		if sl.linkPrimed && in >= sl.linkIn && out >= sl.linkOut { // handle counter resets
			sl.linkRateIn = float64(in-sl.linkIn) / sec
			sl.linkRateOut = float64(out-sl.linkOut) / sec
		}
		sl.linkIn, sl.linkOut, sl.linkPrimed = in, out, true
	}
	for _, f := range []Family{Fam4, Fam6} {
		var maxPeak float64
		live, measured := 0, true
		idle := map[*Slot]bool{}
		for _, sl := range s.slots {
			in, out := sl.bytesIn[f].Load(), sl.bytesOut[f].Load()
			sl.rateIn[f] = float64(in-sl.lastIn[f]) / sec
			sl.rateOut[f] = float64(out-sl.lastOut[f]) / sec
			sl.lastIn[f], sl.lastOut[f] = in, out
			r := sl.rateIn[f] + sl.rateOut[f]
			if r >= 0.05*sl.peak[f] {
				sl.peak[f] = math.Max(r, sl.peak[f]*0.97)
			} else {
				idle[sl] = true
			}
			if !sl.present || sl.ip(f) == nil {
				continue
			}
			live++
			maxPeak = math.Max(maxPeak, sl.peak[f])
		}
		// decay idle link weights back towards capacity
		for sl := range idle {
			if sl.present && sl.ip(f) != nil && sl.peak[f] > 0 {
				sl.peak[f] = math.Min(sl.peak[f]*1.03, math.Max(sl.peak[f], 0.5*maxPeak))
			}
		}
		for _, sl := range s.slots {
			if sl.present && sl.ip(f) != nil && sl.peak[f] < minMeasuredPeak {
				measured = false
			}
		}
		if !s.auto {
			continue
		}
		for _, sl := range s.slots {
			if !sl.present || sl.ip(f) == nil {
				continue
			}
			if live < 2 || !measured {
				sl.weight[f] = 1
			} else {
				sl.weight[f] = math.Max(0.05, sl.peak[f]/maxPeak)
			}
		}
	}
}

// acquire picks ipv4 link for new flow
func (s *Scheduler) Acquire(skip map[string]struct{}) *Slot { return s.AcquireFor(Fam4, skip) }
func (s *Scheduler) Release(sl *Slot)                       { s.ReleaseFor(sl, Fam4) }

// acquirefor picks link matching address family
func (s *Scheduler) AcquireFor(f Family, skip map[string]struct{}) *Slot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	// tiers prioritize probed links before unprobed or failing links
	var tiers [3][]*Slot
	for _, sl := range s.slots {
		if !sl.present || sl.ip(f) == nil {
			continue
		}
		if _, no := skip[sl.key]; no {
			continue
		}
		t := 1
		switch {
		case now.Before(sl.downUntil[f]) || sl.state[f] == stateDown:
			t = 2
		case sl.state[f] == stateUp:
			t = 0
		}
		tiers[t] = append(tiers[t], sl)
	}
	var pool []*Slot
	for _, tier := range tiers {
		if len(tier) > 0 {
			pool = tier
			break
		}
	}
	if len(pool) == 0 {
		return nil
	}
	// random offset prevents synchronization on ties
	start := rand.IntN(len(pool))
	var best *Slot
	bestScore := math.Inf(1)
	for i := range pool {
		sl := pool[(start+i)%len(pool)]
		if score := float64(sl.actF[f].Load()+1) / sl.weight[f]; score < bestScore {
			best, bestScore = sl, score
		}
	}
	best.active.Add(1)
	best.actF[f].Add(1)
	best.total.Add(1)
	return best
}

// v6usable checks if any link supports ipv6
func (s *Scheduler) V6Usable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sl := range s.slots {
		if sl.present && sl.ip(Fam6) != nil && sl.state[Fam6] != stateDown {
			return true
		}
	}
	return false
}

// v6coversall checks if all links support ipv6
func (s *Scheduler) V6CoversAll() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	any6 := false
	for _, sl := range s.slots {
		if !sl.present {
			continue
		}
		has6 := sl.ip(Fam6) != nil && sl.state[Fam6] != stateDown
		if has6 {
			any6 = true
		}
		if sl.ip(Fam4) != nil && sl.state[Fam4] != stateDown && !has6 {
			return false
		}
	}
	return any6
}

func (s *Scheduler) ReleaseFor(sl *Slot, f Family) {
	sl.active.Add(-1)
	sl.actF[f].Add(-1)
}

// setstate updates probe state
func (s *Scheduler) SetState(sl *Slot, f Family, up bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if up {
		sl.state[f] = stateUp
		sl.downUntil[f] = time.Time{}
	} else {
		sl.state[f] = stateDown
	}
}

// fail records link error cooldown
func (s *Scheduler) Fail(sl *Slot, f Family, cooldown time.Duration) {
	s.mu.Lock()
	sl.downUntil[f] = s.now().Add(cooldown)
	s.mu.Unlock()
	sl.failed.Add(1)
}

func (s *Scheduler) Snapshot() []SlotStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SlotStats, 0, len(s.slots))
	for _, sl := range s.slots {
		a := sl.addr.Load()
		st := SlotStats{
			Name: sl.Name, State: "-", State6: "-", Present: sl.present,
			Weight: sl.weight[Fam4], Weight6: sl.weight[Fam6],
			Active: sl.active.Load(), Total: sl.total.Load(), Failed: sl.failed.Load(),
			BytesIn:  sl.bytesIn[Fam4].Load() + sl.bytesIn[Fam6].Load(),
			BytesOut: sl.bytesOut[Fam4].Load() + sl.bytesOut[Fam6].Load(),
			RateIn:   sl.rateIn[Fam4] + sl.rateIn[Fam6], RateOut: sl.rateOut[Fam4] + sl.rateOut[Fam6],
			LinkIn: sl.linkRateIn, LinkOut: sl.linkRateOut,
			Peak: sl.peak[Fam4], Peak6: sl.peak[Fam6],
		}
		if a.ip4 != nil {
			st.IP, st.State = a.ip4.String(), sl.state[Fam4].String()
		}
		if a.ip6 != nil {
			st.IP6, st.State6 = a.ip6.String(), sl.state[Fam6].String()
		}
		out = append(out, st)
	}
	return out
}
