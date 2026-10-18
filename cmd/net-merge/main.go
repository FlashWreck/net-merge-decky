package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"net-merge/internal/engine"
	"net-merge/internal/logx"
	"net-merge/internal/nics"
	"net-merge/internal/platform"
	"net-merge/internal/state"
	"net-merge/internal/tun"
)

const usage = `net-merge: use all your network adapters at once.
decky loader plugin daemon and cli.

credits:
  decky loader plugin: FlashWreck (https://github.com/flashwreck)
  linux port: sidharthify (https://github.com/sidharthify)
  original windows application: rohit267 (https://github.com/rohit267)

usage:
  net-merge daemon     run background aggregation daemon (managed by decky plugin)
  net-merge run        run system-wide in foreground (ctrl+c to stop)
  net-merge on|off     turn aggregation on/off in the running daemon
  net-merge quit       stop aggregation and terminate the daemon
  net-merge status     show whether it is on, and per-adapter speeds (-json for json)
  net-merge list       show detected adapters (-json for json)
  net-merge cleanup    remove leftover routes and tun adapter after crash (root)
  net-merge version    display version and credit info

flags: -state-dir <dir> -include "wlan0,eth0" -weight "wlan0=3" -ipv6 auto|on|off -mtu 9000
`

type weightFlags map[string]float64

func (w weightFlags) String() string { return "" }
func (w weightFlags) Set(v string) error {
	i := strings.LastIndex(v, "=")
	if i < 1 {
		return fmt.Errorf(`want "Name=3", got %q`, v)
	}
	n, err := strconv.ParseFloat(v[i+1:], 64)
	if err != nil || n <= 0 {
		return fmt.Errorf("weight must be > 0 in %q", v)
	}
	w[v[:i]] = n
	return nil
}

type common struct {
	include  string
	weights  weightFlags
	rescan   time.Duration
	stats    time.Duration
	auto     bool
	logFile  string
	stateDir string
	ipv6     string
	mtu      int
}

func (c *common) register(fs *flag.FlagSet) {
	c.weights = weightFlags{}
	fs.StringVar(&c.include, "include", "", "comma-separated adapter names to use (default: all physical)")
	fs.Var(c.weights, "weight", `fixed adapter weight, repeatable: -weight "wlan0=3"`)
	fs.DurationVar(&c.rescan, "rescan", 3*time.Second, "adapter re-scan interval")
	fs.DurationVar(&c.stats, "stats", 0, "stats print interval (0 = off)")
	fs.BoolVar(&c.auto, "auto-weight", true, "weight adapters by measured throughput")
	fs.StringVar(&c.ipv6, "ipv6", "auto", "auto: IPv6 only if every adapter has it; on: always; off: never")
	fs.IntVar(&c.mtu, "mtu", tun.DefaultMTU, "MTU of virtual adapter (1280-65535)")
	fs.StringVar(&c.logFile, "log", "", "write output to this file instead of stdout")
	fs.StringVar(&c.stateDir, "state-dir", state.DefaultDir(), "directory for status and control markers")
}

func (c *common) nicsDetailed() ([]nics.Nic, []nics.Skip, error) {
	var inc []string
	for _, s := range strings.Split(c.include, ",") {
		if s = strings.TrimSpace(s); s != "" {
			inc = append(inc, s)
		}
	}
	return nics.DiscoverNicsDetailed(inc, c.weights)
}

func (c *common) nics() ([]nics.Nic, error) {
	n, _, err := c.nicsDetailed()
	return n, err
}

func (c *common) setup() error {
	if err := os.MkdirAll(c.stateDir, 0o755); err != nil {
		return err
	}
	if c.logFile != "" {
		f, err := os.OpenFile(c.logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		logx.SetOutput(f)
		platform.RedirectStd(f)
	}
	return nil
}

func (c *common) boot() (*engine.Scheduler, *engine.Balancer, error) {
	if c.mtu < 1280 || c.mtu > 65535 {
		return nil, nil, fmt.Errorf("-mtu must be between 1280 and 65535, got %d", c.mtu)
	}
	nicsList, err := c.nics()
	if err != nil {
		return nil, nil, err
	}
	if len(nicsList) == 0 {
		return nil, nil, fmt.Errorf("no usable physical network adapters found")
	}

	sched := engine.NewScheduler(nicsList)
	sched.SetAuto(c.auto && len(c.weights) == 0)
	for _, n := range nicsList {
		logx.Printf("  + %s\n", describeNic(n))
	}
	if len(nicsList) == 1 {
		logx.Printf("  note: only one adapter found, nothing to aggregate yet\n")
	}

	bal := engine.NewBalancer(sched)
	v6, ok := engine.ParseV6Mode(c.ipv6)
	if !ok {
		return nil, nil, fmt.Errorf(`-ipv6 must be auto, on or off, got %q`, c.ipv6)
	}
	bal.SetV6Mode(v6)

	go sched.RunProbes(nil)
	go func() {
		for range time.Tick(c.rescan) {
			if ns, err := c.nics(); err == nil {
				sched.Update(ns)
			}
		}
	}()

	return sched, bal, nil
}

func describeNic(n nics.Nic) string {
	s := n.Name
	if n.IP != nil {
		s += "  v4 " + n.IP.String()
	}
	if n.IP6 != nil {
		s += "  v6 " + n.IP6.String()
	}
	return s
}

func formatNICs(snap []engine.SlotStats) string {
	var sb strings.Builder
	for _, s := range snap {
		gone := ""
		if !s.Present {
			gone = " (gone)"
		}
		fmt.Fprintf(&sb, "%-14s v4 %-11s v6 %-11s link down %6.1f up %5.1f Mbit/s  via net-merge %6.1f  active %3d  total %5d  w %.2f/%.2f  failed %d%s\n",
			s.Name, s.State, s.State6, s.LinkIn*8/1e6, s.LinkOut*8/1e6, (s.RateIn+s.RateOut)*8/1e6, s.Active, s.Total, s.Weight, s.Weight6, s.Failed, gone)
	}
	return sb.String()
}

var version = "1.0.3-decky"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "version":
		fmt.Println("net-merge", version)
		fmt.Println("decky loader plugin: FlashWreck (https://github.com/flashwreck)")
		fmt.Println("linux port: sidharthify (https://github.com/sidharthify)")
		fmt.Println("original windows application: rohit267 (https://github.com/rohit267)")
	case "daemon":
		cmdDaemon(args)
	case "run":
		cmdRun(args)
	case "on", "off", "quit":
		cmdControl(cmd, args)
	case "status":
		cmdStatus(args)
	case "list":
		cmdList(args)
	case "cleanup":
		cmdCleanup()
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func cmdDaemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	var c common
	c.register(fs)
	startOn := fs.Bool("on", true, "start with traffic merging enabled")
	fs.Parse(args)

	if !platform.IsAdmin() {
		die(fmt.Errorf("daemon needs root or CAP_NET_ADMIN privileges"))
	}
	if !platform.SingleInstance() {
		fmt.Println("net-merge daemon is already running.")
		return
	}
	if err := c.setup(); err != nil {
		die(err)
	}
	if err := platform.CheckDriver(); err != nil {
		die(err)
	}

	sched, bal, err := c.boot()
	if err != nil {
		die(err)
	}

	t := tun.New(bal)
	t.MTU = c.mtu

	since := time.Now()
	var lastErr string
	var lastErrVPN bool
	writeStatus := func(now time.Time) {
		snap := sched.Snapshot()
		var totalIn, totalOut float64
		for _, s := range snap {
			if s.Present {
				totalIn += s.RateIn
				totalOut += s.RateOut
			}
		}
		mode := "off"
		if t.IsOn() {
			mode = "system-wide"
		}
		statusErr := lastErr
		if !t.IsOn() {
			if vpnDev, hasVPN := tun.DetectActiveVPN(); hasVPN {
				if statusErr == "" {
					statusErr = fmt.Sprintf("Active VPN detected on %s. Turn off VPN before enabling NetMerge to prevent route conflicts and unencrypted traffic leaks.", vpnDev)
				}
			} else if lastErrVPN {
				lastErr, lastErrVPN = "", false
				statusErr = ""
			}
		}
		st := state.Status{
			PID:          os.Getpid(),
			On:           t.IsOn(),
			Mode:         mode,
			V6:           bal.V6State(),
			V6Mode:       bal.V6Mode().String(),
			Since:        since,
			Updated:      now,
			TotalRateIn:  totalIn,
			TotalRateOut: totalOut,
			NICs:         snap,
			Error:        statusErr,
		}
		state.Write(c.stateDir, st)
	}

	turn := func(on bool) {
		if on {
			if !t.IsOn() {
				if err := t.On(); err != nil {
					lastErr = err.Error()
					lastErrVPN = strings.Contains(strings.ToLower(err.Error()), "vpn")
					logx.Printf("error turning on: %v\n", err)
				} else {
					lastErr = ""
					lastErrVPN = false
					logx.Printf("net-merge is ON: traffic aggregated across links\n")
				}
			}
		} else {
			lastErr = ""
			lastErrVPN = false
			if t.IsOn() {
				t.Off()
				logx.Printf("net-merge is OFF: default routing restored\n")
			}
		}
		writeStatus(time.Now())
	}

	state.TakeMarker(c.stateDir, "off")
	state.TakeMarker(c.stateDir, "quit")
	state.TakeMarker(c.stateDir, "on")

	if *startOn {
		turn(true)
	} else {
		writeStatus(time.Now())
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	lastTick := time.Now()
	for {
		select {
		case s := <-sig:
			logx.Printf("received signal %v, shutting down...\n", s)
			turn(false)
			state.Clear(c.stateDir)
			return
		case now := <-tick.C:
			sched.Tick(now.Sub(lastTick))
			lastTick = now

			switch {
			case state.TakeMarker(c.stateDir, "quit"):
				logx.Printf("received quit marker, shutting down...\n")
				turn(false)
				state.Clear(c.stateDir)
				return
			case state.TakeMarker(c.stateDir, "off"):
				turn(false)
			case state.TakeMarker(c.stateDir, "on"):
				turn(true)
			}

			if state.TakeMarker(c.stateDir, "ipv6-auto") {
				bal.SetV6Mode(engine.V6Auto)
			} else if state.TakeMarker(c.stateDir, "ipv6-on") {
				bal.SetV6Mode(engine.V6On)
			} else if state.TakeMarker(c.stateDir, "ipv6-off") {
				bal.SetV6Mode(engine.V6Off)
			}

			if t.IsOn() {
				if vpnDev, hasVPN := tun.DetectActiveVPN(); hasVPN {
					logx.Printf("active VPN detected on %s, turning net-merge off\n", vpnDev)
					turn(false)
					lastErr = fmt.Sprintf("Active VPN detected on %s. NetMerge was turned off to prevent route conflicts and unencrypted traffic leaks.", vpnDev)
					lastErrVPN = true
				}
			}

			writeStatus(now)
		}
	}
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var c common
	c.register(fs)
	fs.Parse(args)

	if !platform.IsAdmin() {
		die(fmt.Errorf("net-merge run needs root or CAP_NET_ADMIN privileges"))
	}
	if !platform.SingleInstance() {
		fmt.Println("net-merge is already running.")
		return
	}
	if err := c.setup(); err != nil {
		die(err)
	}
	if err := platform.CheckDriver(); err != nil {
		die(err)
	}

	sched, bal, err := c.boot()
	if err != nil {
		die(err)
	}

	t := tun.New(bal)
	t.MTU = c.mtu
	if err := t.On(); err != nil {
		die(err)
	}
	logx.Printf("net-merge is ON: all IPv4 traffic is balanced across adapters. Ctrl+C to stop.\n")

	since := time.Now()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	lastTick := time.Now()
	for {
		select {
		case <-sig:
			goto stop
		case now := <-tick.C:
			sched.Tick(now.Sub(lastTick))
			lastTick = now

			snap := sched.Snapshot()
			var totalIn, totalOut float64
			for _, s := range snap {
				if s.Present {
					totalIn += s.RateIn
					totalOut += s.RateOut
				}
			}
			state.Write(c.stateDir, state.Status{
				PID:          os.Getpid(),
				On:           true,
				Mode:         "system-wide",
				V6:           bal.V6State(),
				V6Mode:       bal.V6Mode().String(),
				Since:        since,
				Updated:      now,
				TotalRateIn:  totalIn,
				TotalRateOut: totalOut,
				NICs:         snap,
			})

			if vpnDev, hasVPN := tun.DetectActiveVPN(); hasVPN {
				logx.Printf("active VPN detected on %s, stopping\n", vpnDev)
				goto stop
			}

			if state.TakeMarker(c.stateDir, "off") || state.TakeMarker(c.stateDir, "quit") {
				goto stop
			}
		}
	}

stop:
	t.Off()
	state.Clear(c.stateDir)
	logx.Printf("net-merge is OFF.\n")
}

func cmdControl(name string, args []string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	dir := fs.String("state-dir", state.DefaultDir(), "state directory")
	fs.Parse(args)

	if !platform.InstanceRunning() {
		if name == "off" || name == "quit" {
			fmt.Println("net-merge is not running.")
			return
		}
		die(fmt.Errorf("net-merge is not running; start daemon first"))
	}
	markerTime := time.Now()
	if err := state.SendMarker(*dir, name); err != nil {
		die(err)
	}

	want := name == "on"
	for i := 0; i < 40; i++ {
		time.Sleep(100 * time.Millisecond)
		st, ok := state.Read(*dir)
		if name == "quit" && !platform.InstanceRunning() {
			fmt.Println("net-merge quit.")
			return
		}
		if ok && st.On == want {
			if want {
				fmt.Println("net-merge is ON.")
			} else {
				fmt.Println("net-merge is OFF.")
			}
			return
		}
		if ok && want && !st.On && st.Error != "" && st.Updated.After(markerTime) {
			die(fmt.Errorf("%s", st.Error))
		}
	}
	fmt.Printf("Command %q sent to daemon.\n", name)
}

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dir := fs.String("state-dir", state.DefaultDir(), "state directory")
	jsonOut := fs.Bool("json", false, "output JSON format")
	fs.Parse(args)

	st, ok := state.Read(*dir)
	running := platform.InstanceRunning()
	if !running && ok && st != nil && st.PID > 0 {
		running = platform.IsPIDRunning(st.PID)
	}

	if *jsonOut {
		if !running || !ok {
			out := map[string]interface{}{
				"running": running,
				"on":      false,
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			return
		}
		b, _ := json.MarshalIndent(st, "", "  ")
		fmt.Println(string(b))
		return
	}

	if !running {
		fmt.Println("net-merge daemon is NOT running.")
		return
	}
	if !ok || !st.On {
		if st != nil && st.Error != "" {
			fmt.Printf("net-merge is OFF (%s)\n", st.Error)
			return
		}
		fmt.Println("net-merge is OFF (daemon is paused).")
		return
	}
	fmt.Printf("net-merge is ON (%s, pid %d, up %s)\nIPv6: %s\n%s",
		st.Mode, st.PID, time.Since(st.Since).Truncate(time.Second), st.V6, formatNICs(st.NICs))
}

type listResult struct {
	Usable  []nics.Nic  `json:"usable"`
	Skipped []nics.Skip `json:"skipped"`
}

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	var c common
	c.register(fs)
	jsonOut := fs.Bool("json", false, "output JSON format")
	fs.Parse(args)

	nicsList, skipped, err := c.nicsDetailed()
	if err != nil {
		die(err)
	}

	if *jsonOut {
		res := listResult{
			Usable:  nicsList,
			Skipped: skipped,
		}
		if res.Usable == nil {
			res.Usable = []nics.Nic{}
		}
		if res.Skipped == nil {
			res.Skipped = []nics.Skip{}
		}
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
		return
	}

	if len(nicsList) == 0 {
		fmt.Println("No usable adapters found.")
	}
	for _, n := range nicsList {
		fmt.Printf("USE   %s  (index %d)\n", describeNic(n), n.Index)
	}
	for _, s := range skipped {
		fmt.Printf("skip  %s: %s\n", s.Name, s.Reason)
	}
}

func cmdCleanup() {
	if !platform.IsAdmin() {
		die(fmt.Errorf("cleanup requires root privileges"))
	}
	tun.CleanupStale()
	fmt.Println("net-merge routes and TUN interface cleaned up.")
}
