# NetMerge Decky Loader Plugin

Aggregate multiple network connections on Steam Deck (OLED and LCD) directly from the Quick Access Menu.

Every new connection (TCP and UDP, IPv4 and IPv6) is assigned to a physical link (internal Wi-Fi, USB-C Ethernet Dock, Phone USB Tethering), enabling multi-connection downloads such as Steam game downloads and updates to run over all available links simultaneously.

<p align="center">
  <img src="assets/banner.png" alt="NetMerge in Gaming Mode" width="720">
</p>

## Credits

- Decky Loader Plugin and Steam Deck Port: [FlashWreck](https://github.com/flashwreck) ([FlashWreck/net-merge-decky](https://github.com/FlashWreck/net-merge-decky))
- Linux Port and Netlink Engine: [sidharthify](https://github.com/sidharthify) ([sidharthify/net-merge](https://github.com/sidharthify/net-merge))
- Original Windows Application: [rohit267](https://github.com/rohit267) ([rohit267/net-merge](https://github.com/rohit267/net-merge))

## Features for Steam Deck

- **Designed for Gaming Mode:** Controlled directly from the Steam Deck Quick Access Menu (`...` button).
- **Multi-Link Speed Boost:** Combines Wi-Fi, Ethernet Dock, and Phone USB Tethering for multi-connection Steam downloads.
- **Live Bandwidth Monitoring:** Real-time speedometer in the Quick Access Menu showing total download and upload speeds along with per-adapter throughput.
- **Dynamic Weighting:** Weights adapters automatically based on measured throughput and ignores links with captive portals or exhausted data.
- **Power Management Aware:** Monitors systemd logind sleep events (`PrepareForSleep`) over D-Bus. Cleanly disables routes before suspend and restores aggregation after wake.
- **No Rootfs Modifications:** Runs as a standalone Decky plugin inside `/home/deck/homebrew/plugins`. Does not disable `steamos-readonly` and does not require package installations.
- **Safe Failover:** Virtual TUN adapter routes are removed cleanly on pause, disable, or plugin unload.

## How It Works

1. **Traffic Capture:** When enabled, NetMerge creates a userland TUN adapter (`net-merge`) and configures netlink routes that intercept outbound flows.
2. **Userland Stack:** Packets entering the TUN are terminated in an efficient userland TCP/IP stack (gVisor / tun2socks).
3. **Egress Binding:** New flows are bound to physical interface source addresses and routed using `SO_BINDTOIFINDEX`, bypassing the TUN to avoid loops.
4. **Balancing:** Flows are distributed using weighted least-active scheduling according to real-time link throughput.

## VPN Compatibility & Limitations

NetMerge aggregates network connections at the physical link level by binding outgoing sockets directly to physical network adapters (`wlan0`, `eth0`, `usb0`).

- **Incompatible with Active VPNs:** If an active VPN tunnel (such as OpenVPN, WireGuard, Tailscale, Mullvad, or ProtonVPN) holds a default (`0.0.0.0/0`) or slash-one (`0.0.0.0/1`, `128.0.0.0/1`) route, NetMerge automatically refuses to turn on and displays a warning:
  > *"Active VPN detected on `<interface>`. Turn off VPN before enabling NetMerge to prevent route conflicts and unencrypted traffic leaks."*
- **Why:** NetMerge captures system traffic via `/1` prefixes. Enabling NetMerge over an active VPN would bypass the encrypted tunnel, leaking unencrypted packets and DNS queries directly over physical adapters, and can destroy existing VPN routes (e.g. OpenVPN `redirect-gateway def1`).
- **No Planned VPN Support:** Multi-link VPN bonding is **not supported and will not be added for the foreseeable future**. Splitting a single standard VPN tunnel across multiple physical IP addresses causes endpoint flapping and packet rejection without a dedicated remote bonding proxy server.

> [!NOTE]
> Always pause or disconnect your VPN before enabling NetMerge aggregation. Once NetMerge is turned off, you can re-enable your VPN as normal.

## Steam Deck Hardware & Kernel Optimizations

NetMerge is purpose-built for SteamOS and the Steam Deck's custom AMD Van Gogh APU:

- **AMD Zen 2 Microarchitecture Vectorization (`GOAMD64=v3`):** Compiled with native AVX2, FMA3, BMI1, and BMI2 vector instructions. Packet handling, memory copies, and userland TCP parsing leverage 256-bit SIMD registers to minimize APU memory bandwidth pressure.
- **BBR Congestion Control:** Outbound connections use Google's BBR congestion control algorithm (`TCP_CONGESTION="bbr"`). Unlike Cubic, BBR does not slash throughput on transient radio drops, maximizing speeds over Wi-Fi and mobile tethering.
- **Low-Latency TCP Socket Flags:** Outbound sockets enforce `TCP_NODELAY` to bypass Nagle's algorithm and `TCP_QUICKACK` to eliminate 40 ms delayed-ACK waits during concurrent Steam CDN chunk negotiations.
- **Dynamic Wi-Fi Power Save Jitter Prevention:** SteamOS enables aggressive 802.11 power saving by default, causing latency to periodically spike from 15 ms to 150+ ms. NetMerge automatically turns Wi-Fi power save off while aggregation is active and restores it when paused or stopped to preserve battery.

## Simple Installation

Follow the standard Decky Loader flow to install:

> **Decky Settings** &rarr; **General** &rarr; Enable **Developer mode** &rarr; Switch to **Desktop mode** &rarr; Download ZIP from GitHub [Releases](https://github.com/FlashWreck/net-merge-decky/releases) &rarr; **Decky Developer options** &rarr; **Install plugin from ZIP file**

1. **Enable Developer Mode:** In the Quick Access Menu (`...`), open Decky Settings &rarr; **General** &rarr; enable **Developer mode**.
2. **Download Release:** Switch to **Desktop mode**, visit the [Releases](https://github.com/FlashWreck/net-merge-decky/releases) page on GitHub, and download `net-merge-decky.zip`.
3. **Install Plugin:** Open Decky Settings &rarr; **Developer** options &rarr; select **Install plugin from ZIP file** and select the downloaded ZIP file.

## Manual Installation on Steam Deck

1. Download `net-merge-decky.zip` from [Releases](https://github.com/FlashWreck/net-merge-decky/releases).
2. Extract the folder to `/home/deck/homebrew/plugins/net-merge-decky`.
3. Restart the Decky Loader daemon:
   ```bash
   sudo systemctl restart plugin_loader
   ```
4. Open the Quick Access Menu (`...` button) in Gaming Mode and open the NetMerge tab.

## Development and Build

Prerequisites on Build Machine:
- Go 1.23 or newer
- Node.js and pnpm (v9 or v11, generates lockfile v9)
- Make

### Build Plugin Locally

```bash
# Build backend Go static binary and frontend Rollup bundle
make all
```

The compiled plugin package will be created at `out/net-merge-decky.zip`.

### Direct Deployment to Steam Deck

```bash
# Deploy to Steam Deck over SSH and restart Decky Loader
make deploy DECK_IP=<your-deck-ip>
```

## Feedback, Testing & Feature Requests

- **Bug Reports:** If an adapter fails to balance or you encounter routing issues, please [open an issue](https://github.com/FlashWreck/net-merge-decky/issues) with your Steam Deck model (LCD/OLED), SteamOS version, and adapter list.
- **Feature Requests:** Have ideas or features in mind? Please share your suggestions in [GitHub Issues](https://github.com/FlashWreck/net-merge-decky/issues).
- **Benchmark Reports:** Help us test! If you test multi-link speeds (e.g. Wi-Fi + 5G Tethering or Wi-Fi + Docked Ethernet), share your before/after download speeds and screenshots.

## License

NetMerge is released under the MIT License. See `LICENSE` and `THIRD-PARTY-NOTICES.md` for full terms.

## AI Disclosure

This project is AI-assisted.
- **Agent used:** Antigravity IDE (Gemini 3.8 Flash (High)) and Claude 

