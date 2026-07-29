const { console: IINAConsole } = iina;
// ─── State ────────────────────────────────────────────────────────────────────
const CAST_PORT = 8009;
const MDNS_SERVICE = "_googlecast._tcp";
const APP_ID = "CC1AD845"; // Default Media Receiver

let devices = []; // [{ name, ip }]
let activeDevice = null; // { name, ip }
let currentUrl = null;
let currentTitle = null;
let castWs = null; // WebSocket to our helper
let castSessionId = null;
let castRequestId = 1;

// ─── Logging ──────────────────────────────────────────────────────────────────
function log(msg) {
  IINAConsole.log(msg);
}

// ─── Status ───────────────────────────────────────────────────────────────────
function setStatus(msg, type) {
  const el = document.getElementById("status");
  el.textContent = msg;
  el.className = type || "";
}

// ─── mDNS Discovery via IINA http API ────────────────────────────────────────
// We query the mDNS-SD HTTP proxy that IINA exposes via its network-request
// permission, falling back to a local avahi/dns-sd shell approach via
// the iina.utils.exec API.
function startScan() {
  setStatus("Scanning local network for Chromecast devices...", "scanning");
  devices = [];
  renderDevices();
  log("Starting mDNS scan for " + MDNS_SERVICE);

  // Use dns-sd via iina exec to discover Chromecast devices
  // dns-sd is built into macOS — no install needed
  iina.postMessage("execScan");

  // Timeout after 5s
  setTimeout(() => {
    btn.disabled = false;
    btn.textContent = "🔍 Scan for Devices";
    if (devices.length === 0) {
      setStatus("No devices found. Are you on the same Wi-Fi?", "error");
      log("Scan complete — no devices found");
    } else {
      setStatus("Found " + devices.length + " device(s). Click to cast.", "");
      log("Scan complete — found " + devices.length + " device(s)");
    }
    renderDevices();
  }, 5000);
}

// Called from main.js when dns-sd output is received
function onDeviceFound(name, ip) {
  if (devices.find((d) => d.ip === ip)) return;
  devices.push({ name, ip });
  log("Found: " + name + " @ " + ip);
  renderDevices();
}

// ─── Render device list ───────────────────────────────────────────────────────
function renderDevices() {
  const container = document.getElementById("devices");
  if (devices.length === 0) {
    container.innerHTML = '<div class="empty">No devices found yet.</div>';
    return;
  }
  container.innerHTML = "";
  devices.forEach((d) => {
    const btn = document.createElement("button");
    btn.className =
      "device-btn" +
      (activeDevice && activeDevice.ip === d.ip ? " active" : "");
    btn.innerHTML =
      '<span style="font-size:20px;">📺</span>' +
      '<span class="device-name">' +
      d.name +
      '<br><span class="device-ip">' +
      d.ip +
      "</span></span>" +
      (activeDevice && activeDevice.ip === d.ip
        ? '<span class="badge">Live</span>'
        : "");
    btn.onclick = () => connectTo(d);
    container.appendChild(btn);
  });
}

// ─── Connect & Cast ───────────────────────────────────────────────────────────
function connectTo(device) {
  activeDevice = device;
  setStatus("Connecting to " + device.name + "...", "scanning");
  log("Connecting to " + device.ip + ":" + CAST_PORT);
  renderDevices();

  // Send connect request to main.js which uses iina.http to open
  // a TLS connection to the Chromecast on port 8009
  iina.postMessage("castConnect", { ip: device.ip, port: CAST_PORT });
}

function onConnected() {
  setStatus("Connected to " + activeDevice.name, "connected");
  document.getElementById("stop-btn").style.display = "block";
  iina.postMessage("castStatus", {
    connected: true,
    deviceName: activeDevice.name,
  });
  log("Connected!");
  renderDevices();
  if (currentUrl) castMedia(currentUrl, currentTitle);
}

function onDisconnected() {
  activeDevice = null;
  castSessionId = null;
  setStatus("Disconnected.");
  document.getElementById("stop-btn").style.display = "none";
  document.getElementById("now-playing").style.display = "none";
  iina.postMessage("castStatus", { connected: false, deviceName: null });
  log("Disconnected");
  renderDevices();
}

function castMedia(url, title) {
  if (!activeDevice) return;
  currentUrl = url;
  currentTitle = title;
  log("Casting: " + url);
  iina.postMessage("castMedia", {
    url,
    title: title || "IINA Video",
    appId: APP_ID,
  });
  document.getElementById("np-title").textContent = title || url;
  document.getElementById("np-url").textContent = url;
  document.getElementById("now-playing").style.display = "block";
  setStatus("Casting: " + (title || "video"), "connected");
}

function stopCasting() {
  iina.postMessage("castStop");
  onDisconnected();
}

// ─── IINA message handlers ────────────────────────────────────────────────────
iina.onMessage("newFile", (d) => {
  currentUrl = d.url;
  currentTitle = d.title;
  if (activeDevice) castMedia(currentUrl, currentTitle);
});

iina.onMessage("playerPause", () => {
  if (activeDevice) iina.postMessage("castPause");
});
iina.onMessage("playerResume", () => {
  if (activeDevice) iina.postMessage("castResume");
});
iina.onMessage("playerSeek", (d) => {
  if (activeDevice) iina.postMessage("castSeek", { position: d.position });
});
iina.onMessage("stopCast", () => stopCasting());
iina.onMessage("requestDevices", () => renderDevices());

// Callbacks from main.js after Cast protocol ops
iina.onMessage("castConnected", () => onConnected());
iina.onMessage("castDisconnected", () => onDisconnected());
iina.onMessage("deviceFound", (d) => onDeviceFound(d.name, d.ip));
iina.onMessage("castLog", (d) => log(d.msg));
