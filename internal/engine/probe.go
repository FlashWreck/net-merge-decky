package engine

import (
	"net"
	"time"

	"net-merge/internal/nics"
)

// reachability probe targets
var probeTargets = [2][]string{
	Fam4: {"1.1.1.1:443", "8.8.8.8:443"},
	Fam6: {"[2606:4700:4700::1111]:443", "[2001:4860:4860::8888]:443"},
}

const (
	probeTimeout   = 2 * time.Second
	probeEveryUp   = 15 * time.Second
	probeEveryDown = 4 * time.Second
)

func probe(sl *Slot, f Family) bool {
	ip := sl.ip(f)
	if ip == nil {
		return false
	}
	network := "tcp4"
	if f == Fam6 {
		network = "tcp6"
	}
	for _, t := range probeTargets[f] {
		d := net.Dialer{LocalAddr: &net.TCPAddr{IP: ip}, Control: nics.ForceEgress(sl.Index, f == Fam6), Timeout: probeTimeout}
		if c, err := d.Dial(network, t); err == nil {
			c.Close()
			return true
		}
	}
	return false
}

type probeJob struct {
	sl *Slot
	f  Family
}

// runprobes checks internet access per adapter and family
func (s *Scheduler) RunProbes(stop <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		for _, j := range s.probeDue() {
			go func() {
				defer j.sl.probing[j.f].Store(false)
				s.SetState(j.sl, j.f, probe(j.sl, j.f))
			}()
		}
	}
}

// probedue returns links due for health verification
func (s *Scheduler) probeDue() []probeJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var due []probeJob
	for _, sl := range s.slots {
		if !sl.present {
			continue
		}
		for _, f := range []Family{Fam4, Fam6} {
			if sl.ip(f) == nil {
				continue
			}
			interval := probeEveryUp
			if sl.state[f] != stateUp {
				interval = probeEveryDown
			}
			if now.Sub(sl.lastProbe[f]) < interval {
				continue
			}
			if sl.probing[f].Swap(true) {
				continue
			}
			sl.lastProbe[f] = now
			due = append(due, probeJob{sl: sl, f: f})
		}
	}
	return due
}
