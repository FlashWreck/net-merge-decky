import {
  ButtonItem,
  DropdownItem,
  Field,
  Focusable,
  Navigation,
  PanelSection,
  PanelSectionRow,
  ProgressBar,
  ToggleField,
  staticClasses,
} from "@decky/ui";
import { addEventListener, callable, definePlugin, removeEventListener, toaster } from "@decky/api";
import { useEffect, useState, useMemo } from "react";
import {
  FaEthernet,
  FaMobileAlt,
  FaNetworkWired,
  FaRedoAlt,
  FaWifi,
} from "react-icons/fa";

interface SlotStats {
  name: string;
  ip: string;
  ip6: string;
  state: string;
  state6: string;
  weight: number;
  weight6: number;
  present: boolean;
  active: number;
  total: number;
  failed: number;
  bytes_in: number;
  bytes_out: number;
  rate_in: number;
  rate_out: number;
  link_in: number;
  link_out: number;
  peak: number;
  peak6: number;
}

interface StatusResponse {
  running: boolean;
  on: boolean;
  pid?: number;
  v6?: string;
  v6_mode?: string;
  total_rate_in?: number;
  total_rate_out?: number;
  nics?: SlotStats[];
  error?: string;
}

interface SettingsResponse {
  enabled_on_boot?: boolean;
  ipv6_mode?: string;
  mtu?: number;
  auto_weight?: boolean;
  include?: string;
  link_notifications?: boolean;
}

// helper to safely extract error message
function getErrorMessage(err: unknown, fallback: string): string {
  if (err instanceof Error) return err.message;
  if (typeof err === "string") return err;
  if (err && typeof err === "object" && "message" in err && typeof (err as { message: unknown }).message === "string") {
    return (err as { message: string }).message;
  }
  return fallback;
}

// callable backend endpoints
const getStatus = callable<[], StatusResponse>("get_status");
const setEnabled = callable<[enabled: boolean], StatusResponse>("set_enabled");
const setIpv6Mode = callable<[mode: string], StatusResponse>("set_ipv6_mode");
const getSettings = callable<[], SettingsResponse>("get_settings");
const saveSettings = callable<[settings: SettingsResponse], SettingsResponse>("save_settings");
const cleanupRoutes = callable<[], boolean>("cleanup_routes");

// helper to describe a link event from the backend
function linkEventText(kind: string, name: string): string {
  switch (kind) {
    case "added":
      return `${name} connected`;
    case "up":
      return `${name} is back online`;
    case "down":
      return `${name} has no internet`;
    case "gone":
      return `${name} disconnected`;
    default:
      return name;
  }
}

// helper to format network throughput
function formatSpeed(bytesPerSec: number): string {
  const mbits = (bytesPerSec * 8) / 1_000_000;
  const mbytes = bytesPerSec / (1024 * 1024);
  if (mbits < 0.1) {
    return "0.0 Mbit/s";
  }
  return `${mbits.toFixed(1)} Mbit/s (${mbytes.toFixed(1)} MB/s)`;
}

// helper to choose icon based on adapter name
function getAdapterIcon(name: string) {
  const n = name.toLowerCase();
  if (n.startsWith("wl") || n.includes("wifi")) {
    return <FaWifi style={{ marginRight: "6px" }} />;
  }
  if (n.startsWith("usb") || n.startsWith("rndis") || n.includes("tether")) {
    return <FaMobileAlt style={{ marginRight: "6px" }} />;
  }
  return <FaEthernet style={{ marginRight: "6px" }} />;
}

function NetMergeContent() {
  const [status, setStatus] = useState<StatusResponse | null>(null);
  const [settings, setSettings] = useState<SettingsResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [cleaning, setCleaning] = useState(false);
  const [errorMsg, setErrorMsg] = useState<string | null>(null);

  // poll status while component is open
  useEffect(() => {
    let mounted = true;

    const fetchInitial = async () => {
      try {
        const [st, cfg] = await Promise.all([getStatus(), getSettings()]);
        if (mounted) {
          setStatus(st);
          setSettings(cfg);
        }
      } catch (e: unknown) {
        if (mounted) {
          setErrorMsg(getErrorMessage(e, "Failed to communicate with backend"));
        }
      }
    };

    fetchInitial();

    const interval = setInterval(() => {
      getStatus()
        .then((st) => {
          if (mounted) {
            setStatus(st);
            if (st.on) {
              setErrorMsg(null);
            }
          }
        })
        .catch(() => {
          // silent catch on polling intervals
        });
    }, 1000);

    return () => {
      mounted = false;
      clearInterval(interval);
    };
  }, []);

  const handleToggle = async (val: boolean) => {
    setLoading(true);
    setErrorMsg(null);
    try {
      const res = await setEnabled(val);
      setStatus(res);
      if (val && !res.on) {
        setErrorMsg(res.error || "Failed to enable aggregation");
      }
    } catch (e: unknown) {
      setErrorMsg(getErrorMessage(e, "Error updating status"));
    } finally {
      setLoading(false);
    }
  };

  const handleIpv6Change = async (mode: string) => {
    try {
      const res = await setIpv6Mode(mode);
      setStatus(res);
      if (settings) {
        setSettings({ ...settings, ipv6_mode: mode });
      }
    } catch (e: unknown) {
      setErrorMsg(getErrorMessage(e, "Error updating IPv6 mode"));
    }
  };

  const handleBootToggle = async (val: boolean) => {
    if (!settings) return;
    const updated = { ...settings, enabled_on_boot: val };
    setSettings(updated);
    try {
      await saveSettings(updated);
    } catch (e: unknown) {
      setErrorMsg(getErrorMessage(e, "Error saving settings"));
    }
  };

  const handleLinkNotifyToggle = async (val: boolean) => {
    if (!settings) return;
    const updated = { ...settings, link_notifications: val };
    setSettings(updated);
    try {
      await saveSettings(updated);
    } catch (e: unknown) {
      setErrorMsg(getErrorMessage(e, "Error saving settings"));
    }
  };

  const handleCleanup = async () => {
    setCleaning(true);
    try {
      await cleanupRoutes();
      const st = await getStatus();
      setStatus(st);
    } catch (e: unknown) {
      setErrorMsg(getErrorMessage(e, "Error during route cleanup"));
    } finally {
      setCleaning(false);
    }
  };

  const activeNICs = useMemo(() => {
    return (status?.nics || []).filter((n) => n.present);
  }, [status?.nics]);

  const totalIn = status?.total_rate_in || 0;
  const totalOut = status?.total_rate_out || 0;

  const vpnBlocked = Boolean(!status?.on && status?.error);
  const displayError = errorMsg || (vpnBlocked ? status?.error : null);

  // status banner
  const statusSection = (
    <PanelSection>
      <PanelSectionRow>
        <ToggleField
          label="Aggregate Connections"
          description={
            vpnBlocked
              ? "VPN Active, Disable VPN to aggregate connections"
              : status?.on
              ? `Merging ${activeNICs.length} Adapter${activeNICs.length === 1 ? "" : "s"}`
              : "Combine Wi-Fi, Ethernet, and Phone Tethering"
          }
          checked={Boolean(status?.on)}
          disabled={loading || vpnBlocked}
          onChange={handleToggle}
        />
      </PanelSectionRow>

      {displayError && (
        <PanelSectionRow>
          <div
            style={{
              color: "#ff6b6b",
              fontSize: "12px",
              padding: "6px 10px",
              background: "rgba(255,107,107,0.12)",
              border: "1px solid rgba(255,107,107,0.25)",
              borderRadius: "4px",
              lineHeight: "1.4",
            }}
          >
            {displayError}
          </div>
        </PanelSectionRow>
      )}
    </PanelSection>
  );

  // live bandwidth meter
  const speedSection = status?.on ? (
    <PanelSection title="Live Speed">
      <PanelSectionRow>
        <Field label="Total Download" bottomSeparator="none">
          <span style={{ fontWeight: "bold", color: "#1a9fff" }}>
            {formatSpeed(totalIn)}
          </span>
        </Field>
      </PanelSectionRow>
      <PanelSectionRow>
        <Field label="Total Upload" bottomSeparator="standard">
          <span style={{ color: "#a0a0a0" }}>{formatSpeed(totalOut)}</span>
        </Field>
      </PanelSectionRow>
    </PanelSection>
  ) : null;

  // connected adapters
  const adaptersSection = (
    <PanelSection title={`Adapters (${activeNICs.length})`}>
      {activeNICs.length === 0 ? (
        <PanelSectionRow>
          <div style={{ color: "#888", fontSize: "13px", padding: "6px 0" }}>
            No Active Network Adapters Detected
          </div>
        </PanelSectionRow>
      ) : (
        activeNICs.map((nic) => {
          const nicIn = nic.rate_in || 0;
          const percentage = totalIn > 0 ? Math.min(100, (nicIn / totalIn) * 100) : 0;
          const isUp = nic.state === "up";

          return (
            <PanelSectionRow key={nic.name}>
              <Focusable
                style={{
                  display: "flex",
                  flexDirection: "column",
                  width: "100%",
                  padding: "6px 8px",
                  borderRadius: "4px",
                }}
              >
                <div
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    alignItems: "center",
                    marginBottom: "4px",
                  }}
                >
                  <span
                    style={{
                      display: "flex",
                      alignItems: "center",
                      fontWeight: "600",
                      fontSize: "14px",
                    }}
                  >
                    {getAdapterIcon(nic.name)}
                    {nic.name}
                  </span>
                  <span
                    style={{
                      fontSize: "11px",
                      padding: "2px 6px",
                      borderRadius: "3px",
                      backgroundColor: isUp ? "rgba(0, 200, 83, 0.2)" : "rgba(255, 171, 0, 0.2)",
                      color: isUp ? "#00e676" : "#ffab00",
                    }}
                  >
                    {nic.state ? nic.state.toUpperCase() : "CHECKING"}
                  </span>
                </div>

                {status?.on && (
                  <>
                    <div
                      style={{
                        display: "flex",
                        justifyContent: "space-between",
                        fontSize: "12px",
                        color: "#ccc",
                        marginBottom: "4px",
                      }}
                    >
                      <span>{formatSpeed(nicIn)}</span>
                      <span style={{ color: "#888" }}>
                        {nic.active} Flow{nic.active === 1 ? "" : "s"}
                      </span>
                    </div>
                    <ProgressBar nProgress={percentage} />
                  </>
                )}

                {!status?.on && (
                  <div style={{ fontSize: "11px", color: "#888" }}>
                    {nic.ip ? `IPv4: ${nic.ip}` : "No IPv4"}
                    {nic.ip6 ? ` | IPv6: ${nic.ip6.slice(0, 16)}...` : ""}
                  </div>
                )}
              </Focusable>
            </PanelSectionRow>
          );
        })
      )}
    </PanelSection>
  );

  // settings
  const settingsSection = (
    <PanelSection title="Settings">
      <PanelSectionRow>
        <DropdownItem
          label="IPv6 Balancing"
          description="Auto-balance IPv6 only when all links support it"
          selectedOption={settings?.ipv6_mode || "auto"}
          rgOptions={[
            { label: "Auto (Recommended)", data: "auto" },
            { label: "Always On", data: "on" },
            { label: "Disabled", data: "off" },
          ]}
          onChange={(opt) => handleIpv6Change(opt.data)}
        />
      </PanelSectionRow>

      <PanelSectionRow>
        <ToggleField
          label="Start on Boot"
          description="Enable aggregation automatically when Steam Deck boots"
          checked={Boolean(settings?.enabled_on_boot)}
          onChange={handleBootToggle}
        />
      </PanelSectionRow>

      <PanelSectionRow>
        <ToggleField
          label="Link Notifications"
          description="Notify when an adapter connects, disconnects or loses internet while aggregation is on"
          checked={settings?.link_notifications !== false}
          onChange={handleLinkNotifyToggle}
        />
      </PanelSectionRow>

      <PanelSectionRow>
        <ButtonItem
          layout="below"
          onClick={handleCleanup}
          disabled={cleaning}
        >
          <FaRedoAlt style={{ marginRight: "6px" }} />
          {cleaning ? "Resetting Routes..." : "Reset Network Routes"}
        </ButtonItem>
      </PanelSectionRow>
    </PanelSection>
  );

  // about
  const aboutSection = (
    <PanelSection title="About">
      <PanelSectionRow>
        <Field
          label="Decky Loader Plugin & Steam Deck Port"
          description="https://github.com/flashwreck"
          focusable
          bottomSeparator="none"
          onClick={() => Navigation.NavigateToExternalWeb("https://github.com/flashwreck")}
        >
          <span style={{ color: "#1a9fff" }}>FlashWreck</span>
        </Field>
      </PanelSectionRow>
      <PanelSectionRow>
        <Field
          label="Linux Port & Netlink Engine"
          description="https://github.com/sidharthify"
          focusable
          bottomSeparator="none"
          onClick={() => Navigation.NavigateToExternalWeb("https://github.com/sidharthify")}
        >
          <span style={{ color: "#1a9fff" }}>sidharthify</span>
        </Field>
      </PanelSectionRow>
      <PanelSectionRow>
        <Field
          label="Original Windows Application"
          description="https://github.com/rohit267"
          focusable
          bottomSeparator="none"
          onClick={() => Navigation.NavigateToExternalWeb("https://github.com/rohit267")}
        >
          <span style={{ color: "#1a9fff" }}>rohit267</span>
        </Field>
      </PanelSectionRow>
    </PanelSection>
  );

  return (
    <div>
      {statusSection}
      {speedSection}
      {adaptersSection}
      {settingsSection}
      {aboutSection}
    </div>
  );
}

export default definePlugin(() => {
  // listener lives here so toasts work while the menu is closed
  const onLinkEvent = (kind: string, name: string) => {
    toaster.toast({ title: "NetMerge", body: linkEventText(kind, name) });
  };
  const linkListener = addEventListener<[kind: string, name: string]>("link_event", onLinkEvent);

  return {
    name: "NetMerge",
    titleView: (
      <div className={staticClasses.Title}>
        <FaNetworkWired style={{ marginRight: "8px" }} />
        NetMerge
      </div>
    ),
    content: <NetMergeContent />,
    icon: <FaNetworkWired />,
    onDismount() {
      removeEventListener("link_event", linkListener);
    },
  };
});
