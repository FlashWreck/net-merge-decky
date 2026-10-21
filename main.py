# python backend for net-merge decky plugin

import asyncio
import errno
import json
import os
import signal
import stat
import subprocess
import time
import decky

MAX_JSON_BYTES = 256 * 1024

def _open_dir_pinned(path: str) -> int:
    # open each component without following links
    if not os.path.isabs(path):
        raise ValueError("absolute path required")
    flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | os.O_CLOEXEC
    fd = os.open("/", flags)
    try:
        for part in (p for p in path.split("/") if p):
            if part in (".", ".."):
                raise ValueError("invalid path component")
            next_fd = os.open(
                part,
                flags | getattr(os, "O_NOFOLLOW", 0),
                dir_fd=fd
            )
            os.close(fd)
            fd = next_fd
        return fd
    except BaseException:
        os.close(fd)
        raise

def _read_json_at(dir_fd: int, name: str, limit: int = MAX_JSON_BYTES):
    # read a json file from a pinned directory
    flags = (
        os.O_RDONLY
        | getattr(os, "O_NOFOLLOW", 0)
        | getattr(os, "O_NONBLOCK", 0)
        | os.O_CLOEXEC
    )
    try:
        fd = os.open(name, flags, dir_fd=dir_fd)
    except FileNotFoundError:
        return None
    except OSError as e:
        if e.errno in (errno.ELOOP, errno.ENXIO):
            return None
        raise
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_size > limit:
            return None
        chunks = []
        total = 0
        while True:
            chunk = os.read(fd, min(65536, limit - total + 1))
            if not chunk:
                break
            total += len(chunk)
            if total > limit:
                return None
            chunks.append(chunk)
        return json.loads(b"".join(chunks))
    finally:
        os.close(fd)

def _write_json_at(dir_fd: int, name: str, data: dict):
    # write a json file through a pinned directory
    tmp_name = f".{name}.{os.getpid()}.{os.urandom(4).hex()}.tmp"
    flags = (
        os.O_WRONLY
        | os.O_CREAT
        | os.O_EXCL
        | getattr(os, "O_NOFOLLOW", 0)
        | os.O_CLOEXEC
    )
    fd = os.open(tmp_name, flags, 0o600, dir_fd=dir_fd)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(data, f, indent=2)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp_name, name, src_dir_fd=dir_fd, dst_dir_fd=dir_fd)
    except BaseException:
        try:
            os.unlink(tmp_name, dir_fd=dir_fd)
        except OSError:
            pass
        raise

def _touch_marker_at(dir_fd: int, name: str):
    # create an empty marker in a pinned directory
    try:
        os.unlink(name, dir_fd=dir_fd)
    except FileNotFoundError:
        pass
    flags = (
        os.O_WRONLY
        | os.O_CREAT
        | os.O_EXCL
        | getattr(os, "O_NOFOLLOW", 0)
        | os.O_CLOEXEC
    )
    fd = os.open(name, flags, 0o600, dir_fd=dir_fd)
    os.close(fd)

def _status_is_live(data: dict, mtime: float) -> bool:
    # check freshness and pid before using status
    if time.time() - mtime > 5.0:
        return False
    pid = data.get("pid")
    if not isinstance(pid, int) or isinstance(pid, bool) or pid <= 0:
        return False
    try:
        os.kill(pid, 0)
        with open(f"/proc/{pid}/comm", "r", encoding="utf-8") as f:
            return f.read().strip().startswith("net-merge")
    except OSError:
        return False

# link notification timings
LINK_POLL_SECS = 1.0
LINK_HOLD_SECS = 5.0
LINK_COOLDOWN_SECS = 20.0
LINK_STATUS_MAX_AGE = 5.0
WAKE_QUIET_SECS = 20.0

class _LinkTracker:

    def __init__(self):
        self.confirmed = {}
        self.pending = {}
        self.last_event = {}
        self.armed = False

    def step(self, observed, now, quiet):
        # observed is None while aggregation is off
        if observed is None:
            self.confirmed.clear()
            self.pending.clear()
            self.armed = False
            return []
        if quiet or not self.armed:
            # adopt current states
            self.confirmed = dict(observed)
            self.pending.clear()
            self.armed = True
            return []

        events = []
        for name in set(self.confirmed) | set(observed):
            seen = observed.get(name, "gone")
            prev = self.confirmed.get(name, "gone")
            if seen == "checking" or seen == prev:
                self.pending.pop(name, None)
                continue
            if prev == "checking" and seen == "up":
                self.confirmed[name] = seen
                self.pending.pop(name, None)
                continue

            cand = self.pending.get(name)
            if cand is None or cand[0] != seen:
                self.pending[name] = (seen, now)
                continue
            if now - cand[1] < LINK_HOLD_SECS:
                continue
            last = self.last_event.get(name)
            if last is not None and now - last < LINK_COOLDOWN_SECS:
                continue

            if seen == "gone":
                kind = "gone"
                self.confirmed.pop(name, None)
            else:
                kind = "down" if seen == "down" else ("added" if prev == "gone" else "up")
                self.confirmed[name] = seen
            self.pending.pop(name, None)
            self.last_event[name] = now
            events.append((kind, name))
        return events

def _link_state(nic: dict) -> str:
    if not nic.get("present", True):
        return "gone"
    states = (nic.get("state"), nic.get("state6"))
    if "up" in states:
        return "up"
    if "no internet" in states:
        return "down"
    return "checking"

class Plugin:
    def __init__(self):
        self.process = None
        self.sleep_task = None
        self.was_on_before_sleep = False
        self.link_task = None
        self.quiet_until = 0.0
        self.default_settings = {
            "enabled_on_boot": False,
            "ipv6_mode": "auto",
            "mtu": 9000,
            "auto_weight": True,
            "include": "",
            "link_notifications": True
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
        # state dir must be root owned when running as root
        if os.geteuid() == 0:
            base = "/run/net-merge-decky"
            os.makedirs(base, mode=0o700, exist_ok=True)
            base_st = os.lstat(base)
            if not stat.S_ISDIR(base_st.st_mode) or base_st.st_uid != 0:
                raise RuntimeError(f"unsafe state base {base}")
            os.chmod(base, 0o700)

            state_dir = os.path.join(base, "state")
            try:
                os.mkdir(state_dir, 0o700)
            except FileExistsError:
                pass
            state_st = os.lstat(state_dir)
            if not stat.S_ISDIR(state_st.st_mode) or state_st.st_uid != 0:
                raise RuntimeError(f"unsafe state dir {state_dir}")
            os.chmod(state_dir, 0o700)
            return state_dir

        base = os.path.join(os.path.expanduser("~/.cache"), f"net-merge-{os.getuid()}")
        state_dir = os.path.join(base, "state")
        os.makedirs(state_dir, mode=0o700, exist_ok=True)
        return state_dir

    def _get_settings_dir(self) -> str:
        settings_dir = getattr(decky, "DECKY_PLUGIN_SETTINGS_DIR", None)
        if settings_dir:
            # decky creates the settings directory
            return settings_dir
        if os.geteuid() == 0:
            settings_dir = "/run/net-merge-decky/settings"
            os.makedirs(settings_dir, mode=0o700, exist_ok=True)
            return settings_dir

        uid = os.getuid() if hasattr(os, "getuid") else 0
        base = os.path.expanduser("~/.cache")
        settings_dir = os.path.join(base, f"net-merge-settings-{uid}")
        os.makedirs(settings_dir, mode=0o700, exist_ok=True)
        return settings_dir

    def _load_settings(self) -> dict:
        # read settings through a pinned directory
        try:
            dir_fd = _open_dir_pinned(self._get_settings_dir())
            try:
                data = _read_json_at(dir_fd, "settings.json")
            finally:
                os.close(dir_fd)
            if isinstance(data, dict):
                res = self.default_settings.copy()
                res.update(data)
                return res
        except Exception as e:
            decky.logger.error(f"failed to load settings: {e}")
        return self.default_settings.copy()

    def _save_settings_disk(self, s: dict):
        try:
            dir_fd = _open_dir_pinned(self._get_settings_dir())
            try:
                _write_json_at(dir_fd, "settings.json", s)
            finally:
                os.close(dir_fd)
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
        try:
            state_dir = self._get_state_dir()
        except Exception as e:
            decky.logger.error(f"failed to resolve state dir: {e}")
            state_dir = None

        if state_dir:
            try:
                dir_fd = _open_dir_pinned(state_dir)
                try:
                    _touch_marker_at(dir_fd, "quit")
                finally:
                    os.close(dir_fd)
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
        data = None
        mtime = 0.0
        try:
            state_dir = self._get_state_dir()
            dir_fd = _open_dir_pinned(state_dir)
            try:
                st = os.stat(
                    "status.json",
                    dir_fd=dir_fd,
                    follow_symlinks=False
                )
                if stat.S_ISREG(st.st_mode):
                    mtime = st.st_mtime
                    data = _read_json_at(dir_fd, "status.json")
            finally:
                os.close(dir_fd)
        except FileNotFoundError:
            pass
        except Exception as e:
            decky.logger.error(f"error reading status file: {e}")

        if isinstance(data, dict) and _status_is_live(data, mtime):
            data["running"] = True
            return data

        # if no live status file is present
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
            try:
                dir_fd = _open_dir_pinned(state_dir)
                try:
                    _touch_marker_at(dir_fd, marker_name)
                finally:
                    os.close(dir_fd)
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
        marker_name = f"ipv6-{mode}"
        try:
            dir_fd = _open_dir_pinned(state_dir)
            try:
                _touch_marker_at(dir_fd, marker_name)
            finally:
                os.close(dir_fd)
        except Exception as e:
            decky.logger.error(f"failed to write marker {marker_name}: {e}")

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

    def _read_link_states(self):
        # returns None when aggregation is off or status is stale
        data = None
        try:
            dir_fd = _open_dir_pinned(self._get_state_dir())
            try:
                st = os.stat(
                    "status.json",
                    dir_fd=dir_fd,
                    follow_symlinks=False
                )
                if not stat.S_ISREG(st.st_mode):
                    return None
                if time.time() - st.st_mtime > LINK_STATUS_MAX_AGE:
                    return None
                data = _read_json_at(dir_fd, "status.json")
            finally:
                os.close(dir_fd)
        except Exception:
            return None

        if not isinstance(data, dict) or not data.get("on"):
            return None
        nics = data.get("nics")
        if not isinstance(nics, list):
            return None
        return {
            n["name"]: _link_state(n)
            for n in nics
            if isinstance(n, dict) and isinstance(n.get("name"), str)
        }

    async def _link_monitor(self):
        tracker = _LinkTracker()
        try:
            while True:
                await asyncio.sleep(LINK_POLL_SECS)
                try:
                    now = time.monotonic()
                    observed = self._read_link_states()
                    events = tracker.step(observed, now, now < self.quiet_until)
                    if events and self._load_settings().get("link_notifications", True):
                        for kind, name in events:
                            await decky.emit("link_event", kind, name)
                except Exception as e:
                    decky.logger.error(f"link monitor error: {e}")
        except asyncio.CancelledError:
            pass

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
                    self.quiet_until = float("inf")
                    st = await self.get_status()
                    self.was_on_before_sleep = bool(st.get("on", False))
                    if self.was_on_before_sleep:
                        await self.set_enabled(False)
                elif "boolean false" in text:
                    # system woke up
                    decky.logger.info("system resumed from sleep")
                    self.quiet_until = time.monotonic() + WAKE_QUIET_SECS
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
        self.link_task = asyncio.create_task(self._link_monitor())

        # auto-start daemon if configured
        if settings.get("enabled_on_boot", False):
            await self._start_daemon(start_on=True)
            self._set_wifi_low_latency(True)

    async def _unload(self):
        decky.logger.info("net-merge plugin backend unloading")
        self._set_wifi_low_latency(False)
        if self.sleep_task:
            self.sleep_task.cancel()
        if self.link_task:
            self.link_task.cancel()
        await self._stop_daemon()

    async def _uninstall(self):
        decky.logger.info("net-merge plugin backend uninstalling")
        self._set_wifi_low_latency(False)
        await self.cleanup_routes()
