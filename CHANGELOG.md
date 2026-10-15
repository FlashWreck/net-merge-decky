# Changelog

What changed in each release. Each version's section becomes its GitHub release notes, so add the
entry here before pushing a version tag.

## [Unreleased]

### Added
- **Linux support**, including NixOS. System-wide mode, the tray icon (any desktop with
  StatusNotifierItem support: KDE, GNOME with the AppIndicator extension, XFCE, Cinnamon, ...), the
  SOCKS5 proxy, live per-adapter speeds and autostart (XDG autostart entry) all work. The TUN and
  its routes are set up over netlink, flows are pinned with `SO_BINDTOIFINDEX`, and adapters are
  detected from sysfs and the routing tables. Needs root or `CAP_NET_ADMIN` (`setcap`).
- **Nix flake** with a package and a NixOS module: `programs.net-merge.enable` installs a capability
  wrapper so the tray runs without root, and `programs.net-merge.autostart` starts it in every
  graphical session.

### Changed
- The source is split into packages: `cmd/net-merge` (the command) and `internal/...` (engine, nics,
  tun, tray, platform, state, icon, logx). Build with `go build ./cmd/net-merge`.

## [0.1.1] - 2026-10-14

### Added
- **Speeds on hover.** Hovering the tray icon shows each adapter's download/upload speed and a Total
  line. The numbers come from Windows' own adapter counters, so every app counts, including apps that
  bypass net-merge (qBittorrent binds to adapters directly), and they show while paused too.
- **Real speeds in the menu.** The right-click menu shows each adapter's speed, how many connections
  net-merge put on it, and whether it has IPv6.
- `net-merge status` shows each adapter's total traffic ("link") next to the part net-merge relayed.
- `-mtu` flag to change the virtual adapter's MTU (default 9000).
- `-pprof` flag to serve Go's profiler on a localhost address, for diagnosing CPU use.
- README: screenshots, features and how to use.

### Changed
- **About 3x less CPU.** The virtual adapter now runs at MTU 9000 instead of 1500, so about 5x fewer
  packets go through the userland network stack. Measured on the same machine and links: 0.54 → 0.18
  CPU cores per 100 Mbit/s.
- Tray wording: **Merging** / **Paused**, and *Pause merging* / *Start merging* in the menu (was ON/OFF
  and *Turn on* / *Turn off*).
- Idle UDP sessions end after 30 seconds instead of 60, which means fewer sockets and less memory with
  torrent clients.
- Quieter log: torrent traffic no longer writes a warning for every dropped packet.

### Fixed
- **Tray icon missing after sign-in.** With "start at sign-in" on, the tray could start before the
  taskbar existed and stay invisible and inactive. It now waits for the taskbar, and the sign-in task
  retries if the tray fails to start. If you enabled autostart in 0.1.0, install 0.1.1 with the option
  ticked to update it.
- UDP datagrams larger than 1500 bytes going through the virtual adapter were silently dropped.
- Tooltip cut off after "Ethernet 4 ↓": Windows 11 shows only 64 characters of a tray tooltip, so the
  text now fits that limit.
- A garbled "…" appeared when long tooltip text was shortened.
- Errors from the tray library were not written to the log in the windowless build.

## [0.1.0] - 2026-10-13

First release.

### Added
- System-wide per-connection balancing: every new TCP and UDP connection, IPv4 and IPv6, is pinned to
  one adapter, so parallel connections use Wi-Fi, Ethernet and phone/USB tethers at the same time.
- Tray app: left-click turns it on or off, right-click shows per-adapter speeds, optional start at
  sign-in without a UAC prompt.
- Automatic adapter detection: real hardware with a default gateway is used, while VPN and virtual adapters
  (Tailscale, WireGuard, Hyper-V, WSL) are skipped; `net-merge list` shows why.
- Per-adapter internet checks: a link without internet gets no connections until it recovers.
- Connections are spread by each adapter's measured speed, separately for IPv4 and IPv6.
- IPv6 policy (`-ipv6 auto|on|off`): when only some adapters have IPv6, apps are steered to IPv4 so
  every adapter stays in use.
- Crash-safe: the virtual adapter and its routes disappear with the process.
- Command line: `on`, `off`, `quit`, `status`, `list`, `autostart`, `cleanup`, plus a SOCKS5 mode
  that needs no admin.
- Installer and portable zip.
