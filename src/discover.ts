// discover.ts
// IINA plugin helper: discover + resolve Chromecast (_googlecast._tcp) devices
// via macOS's built-in dns-sd, extracting the full TXT/SRV metadata.
//
// Requires the "file-system" permission in Info.json (needed for utils.exec).

const { utils, console } = iina;

// Decoded capability bits from the `ca` TXT field. Only the low I/O bits are
// reliably documented across firmware; `raw` preserves the full value.
type Capabilities = {
  videoOut: boolean;
  videoIn: boolean;
  audioOut: boolean;
  audioIn: boolean;
  raw: number;
};

type DiscoverResult = {
  name: string; // friendly name (fn), falling back to instance/hostname
  hostname: string; // SRV target, e.g. "abcdef.local"
  ip: string | null; // resolved IPv4
  port: number; // SRV port (usually 8009)
  id: string | null; // stable device UUID (id) — best key to dedupe/remember
  model: string | null; // device type / model (md), e.g. "Chromecast Ultra"
  deviceType: "video" | "audio" | "group" | "unknown";
  isActive: boolean; // st != 0 -> an app/session is running
  statusText: string; // rs -> receiver status text (may be stale/empty; see notes)
  version: string | null; // ve -> TXT record version
  iconPath: string | null; // ic
  capabilities: Capabilities | null; // decoded ca bitmask
  txt: Record<string, string>; // every raw TXT key=value pair (forward-compat)
};

const DNS_SD = "/usr/bin/dns-sd";
const SERVICE = "_googlecast._tcp";
const TIMEOUT_SEC = 3; // dns-sd streams forever; -t makes it exit so exec() resolves

// --- capability bitmask (ca) -------------------------------------------------
const CAP = {
  VIDEO_OUT: 1 << 0, // 1
  VIDEO_IN: 1 << 1, // 2
  AUDIO_OUT: 1 << 2, // 4
  AUDIO_IN: 1 << 3, // 8
};

function decodeCapabilities(ca: string | undefined): Capabilities | null {
  if (ca === undefined) return null;
  const n = parseInt(ca, 10);
  if (Number.isNaN(n)) return null;
  return {
    videoOut: (n & CAP.VIDEO_OUT) !== 0,
    videoIn: (n & CAP.VIDEO_IN) !== 0,
    audioOut: (n & CAP.AUDIO_OUT) !== 0,
    audioIn: (n & CAP.AUDIO_IN) !== 0,
    raw: n,
  };
}

// Classify by capabilities first (robust), model string second.
function classify(
  md: string | undefined,
  caps: Capabilities | null
): DiscoverResult["deviceType"] {
  if (md && /group/i.test(md)) return "group";
  if (caps) return caps.videoOut ? "video" : "audio";
  return "unknown";
}

// dns-sd escapes special chars in DNS labels (space -> \032, '.' -> \., '\' -> \\).
function unescapeDNSLabel(s: string): string {
  return s
    .replace(/\\(\d{3})/g, (_, d) => String.fromCharCode(parseInt(d, 10)))
    .replace(/\\(.)/g, "$1");
}

type RawDevice = {
  port?: number;
  hostname?: string;
  instanceName?: string;
  txt?: Record<string, string>;
};

// Parse `dns-sd -Z` zone output into a map keyed by instance full name.
// SRV -> hostname + port; TXT -> every key=value pair (not just fn).
function parseZone(stdout: string): Record<string, RawDevice> {
  const devices: Record<string, RawDevice> = {};
  for (const line of stdout.split("\n")) {
    // SRV: "<name> SRV <prio> <weight> <port> <target>"
    let m = line.match(/^(\S+)\s+SRV\s+\d+\s+\d+\s+(\d+)\s+(\S+)/);
    if (m) {
      const d = devices[m[1]] || (devices[m[1]] = {});
      d.port = parseInt(m[2], 10);
      d.hostname = m[3].replace(/\.$/, ""); // strip trailing dot
      d.instanceName = unescapeDNSLabel(m[1].split(/(?<!\\)\./)[0]);
      continue;
    }
    // TXT: '<name> TXT "k=v" "k=v" ...'
    m = line.match(/^(\S+)\s+TXT\s+(.*)$/);
    if (m) {
      const d = devices[m[1]] || (devices[m[1]] = {});
      const txt = d.txt || (d.txt = {});
      const pairs = m[2].match(/"([^"]*)"/g) || [];
      for (const raw of pairs) {
        const kv = raw.slice(1, -1); // strip surrounding quotes
        const eq = kv.indexOf("="); // split on FIRST '=' only (values may contain '=')
        if (eq === -1) {
          txt[kv] = "";
        } else {
          txt[kv.slice(0, eq)] = kv.slice(eq + 1);
        }
      }
    }
  }
  return devices;
}

// Pull the first IPv4 out of `dns-sd -G v4` output (first "Add" line with an address).
function parseIP(stdout: string): string | null {
  for (const line of stdout.split("\n")) {
    if (!/\bAdd\b/.test(line)) continue;
    const m = line.match(/\b(\d{1,3}(?:\.\d{1,3}){3})\b/);
    if (m) return m[1];
  }
  return null;
}

async function resolveIP(hostname: string): Promise<string | null> {
  try {
    // NOTE: -t must come BEFORE the operation flag (dns-sd parses modifiers first).
    const { stdout } = await utils.exec(DNS_SD, [
      "-t",
      String(TIMEOUT_SEC),
      "-G",
      "v4",
      hostname,
    ]);
    return parseIP(stdout);
  } catch (e) {
    console.log(`dns-sd -G failed for ${hostname}: ${e}`);
    return null;
  }
}

// Main entry: returns the enriched device list.
async function discoverChromecasts(): Promise<DiscoverResult[]> {
  let zone: Record<string, RawDevice>;
  try {
    const { stdout } = await utils.exec(DNS_SD, [
      "-t",
      String(TIMEOUT_SEC),
      "-Z",
      SERVICE,
      "local",
    ]);
    zone = parseZone(stdout);
  } catch (e) {
    console.log(`dns-sd -Z failed: ${e}`);
    return [];
  }

  const entries = Object.values(zone).filter((d) => d.hostname);

  // Resolve all hostnames to IPs concurrently.
  return Promise.all(
    entries.map(async (d) => {
      const txt = d.txt || {};
      const caps = decodeCapabilities(txt["ca"]);
      const st = parseInt(txt["st"] ?? "", 10);
      return {
        name: txt["fn"] || d.instanceName || d.hostname!,
        hostname: d.hostname!,
        ip: await resolveIP(d.hostname!),
        port: d.port ?? 8009,
        id: txt["id"] ?? null,
        model: txt["md"] ?? null,
        deviceType: classify(txt["md"], caps),
        isActive: !Number.isNaN(st) && st !== 0,
        statusText: txt["rs"] ?? "",
        version: txt["ve"] ?? null,
        iconPath: txt["ic"] ?? null,
        capabilities: caps,
        txt, // raw map: anything not modeled above is still here
      };
    })
  );
}

// Example:
// discoverChromecasts().then((d) => console.log(JSON.stringify(d, null, 2)));

export { discoverChromecasts, DiscoverResult, Capabilities };
