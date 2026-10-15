# python backend for net-merge decky plugin

import asyncio
import json
import os
import signal
import subprocess
import decky

class Plugin:
    def __init__(self):
        self.process = None
        self.sleep_task = None
        self.was_on_before_sleep = False
        self.default_settings = {
            "enabled_on_boot": False,
            "ipv6_mode": "auto",
            "mtu": 9000,
            "auto_weight": True,
            "include": ""
        }

    def _get_wifi_interfaces(self) -> list:
        # find active wireless interfaces
        interfaces = []
        try:
            res = subprocess.run(["iw", "dev"], capture_output=True, text=True, check=False)
            if res.returncode == 0:
                for line in res.stdout.splitlines():
                    line = line.strip()
                    if line.startswith("Interface "):
                        interfaces.append(line.split()[1])
        except Exception:
            pass
        if not interfaces and os.path.exists("/sys/class/net/wlan0"):
            interfaces.append("wlan0")
        return interfaces

    def _set_wifi_low_latency(self, active: bool):
        # toggle wifi power save to prevent latency jitter
        state = "off" if active else "on"
        for iface in self._get_wifi_interfaces():
            try:
                subprocess.run(
                    ["iw", "dev", iface, "set", "power_save", state],
                    capture_output=True,
                    check=False,
                    timeout=2
                )
                decky.logger.info(f"set wifi power_save {state} on {iface}")
            except Exception as e:
                decky.logger.error(f"failed to set wifi power_save on {iface}: {e}")

    def _get_bin_path(self) -> str:
        plugin_dir = getattr(decky, "DECKY_PLUGIN_DIR", os.path.dirname(os.path.abspath(__file__)))
        return os.path.join(plugin_dir, "bin", "net-merge")

    def _get_state_dir(self) -> str:
        runtime_dir = getattr(decky, "DECKY_PLUGIN_RUNTIME_DIR", "/tmp/net-merge-decky")
        state_dir = os.path.join(runtime_dir, "state")
        os.makedirs(state_dir, exist_ok=True)
        return state_dir

    def _get_settings_path(self) -> str:
        settings_dir = getattr(decky, "DECKY_PLUGIN_SETTINGS_DIR", "/tmp/net-merge-decky")
        os.makedirs(settings_dir, exist_ok=True)
        return os.path.join(settings_dir, "settings.json")

    def _load_settings(self) -> dict:
        path = self._get_settings_path()
        try:
            if os.path.exists(path):
                with open(path, "r", encoding="utf-8") as f:
                    data = json.load(f)
                    res = self.default_settings.copy()
                    res.update(data)
                    return res
        except Exception as e:
            decky.logger.error(f"failed to load settings: {e}")
        return self.default_settings.copy()

    def _save_settings_disk(self, s: dict):
        path = self._get_settings_path()
        try:
            with open(path, "w", encoding="utf-8") as f:
                json.dump(s, f, indent=2)
        except Exception as e:
            decky.logger.error(f"failed to save settings: {e}")

    async def get_settings(self) -> dict:
        return self._load_settings()

    async def save_settings(self, new_settings: dict) -> dict:
        s = self._load_settings()
        s.update(new_settings)
        self._save_settings_disk(s)
        return s

    async def _start_daemon(self, start_on: bool = True):
        # ensure binary is executable
        bin_path = self._get_bin_path()
        if not os.path.exists(bin_path):
            decky.logger.error(f"binary not found at {bin_path}")
            return False

        try:
            os.chmod(bin_path, 0o755)
        except Exception:
            pass

        state_dir = self._get_state_dir()
        settings = self._load_settings()

        cmd = [
            bin_path,
            "daemon",
            "-state-dir", state_dir,
            "-ipv6", settings.get("ipv6_mode", "auto"),
            "-mtu", str(settings.get("mtu", 9000))
        ]

        if not start_on:
            cmd.extend(["-on=false"])

        include = settings.get("include", "").strip()
        if include:
            cmd.extend(["-include", include])

        decky.logger.info(f"starting daemon: {' '.join(cmd)}")
        try:
            self.process = subprocess.Popen(
                cmd,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL
            )
            return True
        except Exception as e:
            decky.logger.error(f"failed to start daemon process: {e}")
            return False

    async def _stop_daemon(self):
        state_dir = self._get_state_dir()
        # send quit marker
        quit_marker = os.path.join(state_dir, "quit")
        try:
            with open(quit_marker, "w") as f:
                pass
        except Exception:
            pass

        # wait for process to finish
        if self.process:
            for _ in range(20):
                if self.process.poll() is not None:
                    break
                await asyncio.sleep(0.1)

            if self.process.poll() is None:
                try:
                    self.process.terminate()
                    await asyncio.sleep(0.2)
                    if self.process.poll() is None:
                        self.process.kill()
                except Exception:
                    pass
            self.process = None

    async def get_status(self) -> dict:
        state_dir = self._get_state_dir()
        status_file = os.path.join(state_dir, "status.json")

        if os.path.exists(status_file):
            try:
                with open(status_file, "r", encoding="utf-8") as f:
                    data = json.load(f)
                    data["running"] = True
                    return data
            except Exception as e:
                decky.logger.error(f"error reading status file: {e}")

        # if no status file is present
        return {
            "running": self.process is not None and self.process.poll() is None,
            "on": False,
            "total_rate_in": 0.0,
            "total_rate_out": 0.0,
            "nics": []
        }

    async def set_enabled(self, enabled: bool) -> dict:
        state_dir = self._get_state_dir()
        status = await self.get_status()

        if not status.get("running"):
            if enabled:
                await self._start_daemon(start_on=True)
                for _ in range(25):
                    await asyncio.sleep(0.1)
                    st = await self.get_status()
                    if st.get("on") == enabled or st.get("error"):
                        break
        else:
            marker_name = "on" if enabled else "off"
            marker_path = os.path.join(state_dir, marker_name)
            try:
                with open(marker_path, "w") as f:
                    pass
            except Exception as e:
                decky.logger.error(f"failed to write marker {marker_name}: {e}")

            # poll until daemon processes marker or reports error
            for _ in range(25):
                await asyncio.sleep(0.1)
                st = await self.get_status()
                if st.get("on") == enabled:
                    break
                if enabled and st.get("error"):
                    break

        st = await self.get_status()
        self._set_wifi_low_latency(bool(st.get("on", False)))
        return st

    async def set_ipv6_mode(self, mode: str) -> dict:
        if mode not in ["auto", "on", "off"]:
            mode = "auto"
        s = self._load_settings()
        s["ipv6_mode"] = mode
        self._save_settings_disk(s)

        state_dir = self._get_state_dir()
        marker_path = os.path.join(state_dir, f"ipv6-{mode}")
        try:
            with open(marker_path, "w") as f:
                pass
        except Exception as e:
            decky.logger.error(f"failed to write marker ipv6-{mode}: {e}")

        await asyncio.sleep(0.2)
        return await self.get_status()

    async def get_adapters(self) -> dict:
        bin_path = self._get_bin_path()
        if not os.path.exists(bin_path):
            return {"usable": [], "skipped": []}

        try:
            res = await asyncio.to_thread(
                subprocess.run,
                [bin_path, "list", "-json"],
                capture_output=True,
                text=True,
                check=False,
                timeout=5
            )
            if res.returncode == 0 and res.stdout.strip():
                return json.loads(res.stdout)
        except Exception as e:
            decky.logger.error(f"failed to list adapters: {e}")

        return {"usable": [], "skipped": []}

    async def cleanup_routes(self) -> bool:
        self._set_wifi_low_latency(False)
        bin_path = self._get_bin_path()
        if not os.path.exists(bin_path):
            return False
        try:
            await self._stop_daemon()
            res = await asyncio.to_thread(
                subprocess.run,
                [bin_path, "cleanup"],
                capture_output=True,
                text=True,
                check=False,
                timeout=5
            )
            return res.returncode == 0
        except Exception as e:
            decky.logger.error(f"failed to cleanup routes: {e}")
            return False

    async def _sleep_monitor(self):
        # listen for prepare for sleep signals on dbus
        try:
            proc = await asyncio.create_subprocess_exec(
                "dbus-monitor",
                "--system",
                "type='signal',interface='org.freedesktop.login1.Manager',member='PrepareForSleep'",
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.DEVNULL
            )
            while True:
                line = await proc.stdout.readline()
                if not line:
                    break
                text = line.decode("utf-8", errors="ignore")
                if "boolean true" in text:
                    # system going to sleep
                    decky.logger.info("system entering sleep, pausing net-merge")
                    st = await self.get_status()
                    self.was_on_before_sleep = bool(st.get("on", False))
                    if self.was_on_before_sleep:
                        await self.set_enabled(False)
                elif "boolean false" in text:
                    # system woke up
                    decky.logger.info("system resumed from sleep")
                    if self.was_on_before_sleep:
                        # wait for wifi and ethernet to reconnect
                        await asyncio.sleep(3.0)
                        decky.logger.info("restoring net-merge aggregation after sleep")
                        await self.set_enabled(True)
        except asyncio.CancelledError:
            pass
        except Exception as e:
            decky.logger.error(f"dbus sleep monitor error: {e}")

    async def _main(self):
        decky.logger.info("net-merge plugin backend initializing")
        settings = self._load_settings()

        # start sleep monitor in background
        self.sleep_task = asyncio.create_task(self._sleep_monitor())

        # auto-start daemon if configured
        if settings.get("enabled_on_boot", False):
            await self._start_daemon(start_on=True)
            self._set_wifi_low_latency(True)

    async def _unload(self):
        decky.logger.info("net-merge plugin backend unloading")
        self._set_wifi_low_latency(False)
        if self.sleep_task:
            self.sleep_task.cancel()
        await self._stop_daemon()

    async def _uninstall(self):
        decky.logger.info("net-merge plugin backend uninstalling")
        self._set_wifi_low_latency(False)
        await self.cleanup_routes()
