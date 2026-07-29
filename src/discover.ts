// discover-chromecasts.js
// IINA plugin helper: discover + resolve Chromecast (_googlecast._tcp) devices
// via macOS's built-in dns-sd. Returns: [{ name, hostname, ip }, ...]
//
// Requires the "file-system" permission in Info.json (needed for utils.exec).

const { utils, console } = iina;

type DiscoverResult = {
  name: string;
  hostname: string;
  ip: string;
};

const DNS_SD = "/usr/bin/dns-sd";
const SERVICE = "_googlecast._tcp";
const TIMEOUT_SEC = 3; // dns-sd streams forever; -t makes it exit so exec() can resolve

// dns-sd escapes special chars in DNS labels (space -> \032, '.' -> \., '\' -> \\).
function unescapeDNSLabel(s) {
  return s
    .replace(/\\(\d{3})/g, (_, d) => String.fromCharCode(parseInt(d, 10)))
    .replace(/\\(.)/g, "$1");
}

// Parse `dns-sd -Z` zone-format output into a map keyed by instance full name.
// Grabs hostname + port from SRV lines and the friendly name from the TXT "fn=" key.
function parseZone(stdout) {
  const devices = {};
  for (const line of stdout.split("\n")) {
    let m = line.match(/^(\S+)\s+SRV\s+\d+\s+\d+\s+(\d+)\s+(\S+)/);
    if (m) {
      const d = devices[m[1]] || (devices[m[1]] = {});
      d.port = parseInt(m[2], 10);
      d.hostname = m[3].replace(/\.$/, ""); // strip trailing dot
      d.instanceName = unescapeDNSLabel(m[1].split(/(?<!\\)\./)[0]);
      continue;
    }
    m = line.match(/^(\S+)\s+TXT\s+(.*)$/);
    if (m) {
      const d = devices[m[1]] || (devices[m[1]] = {});
      const fn = m[2].match(/"fn=([^"]*)"/); // Chromecast friendly name
      if (fn) d.friendlyName = fn[1];
    }
  }
  return devices;
}

// Pull the first IPv4 out of `dns-sd -G v4` output (first "Add" line with an address).
function parseIP(stdout) {
  for (const line of stdout.split("\n")) {
    if (!/\bAdd\b/.test(line)) continue;
    const m = line.match(/\b(\d{1,3}(?:\.\d{1,3}){3})\b/);
    if (m) return m[1];
  }
  return null;
}

async function resolveIP(hostname) {
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

// Main entry: returns [{ name: string, hostname: string, ip: string|null }, ...]
async function discoverChromecasts(): Promise<DiscoverResult[]> {
  let zone;
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
  const results = await Promise.all(
    entries.map(async (d) => ({
      name: d.friendlyName || d.instanceName || d.hostname,
      hostname: d.hostname,
      ip: await resolveIP(d.hostname),
    }))
  );

  return results;
}

// Example usage:
// discoverChromecasts().then((devices) => console.log(JSON.stringify(devices, null, 2)));

export { discoverChromecasts };
