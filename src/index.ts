import { discoverChromecasts, DiscoverResult } from "./discover";
import { downloadCastHelper } from "./download-helper";

const {
  standaloneWindow,
  overlay,
  sidebar,
  global,
  mpv,
  core,
  event: IINAEvent,
  console: IINAConsole,
  menu,
} = iina;

IINAConsole.log("Plugin is running");

IINAEvent.on("iina.window-loaded", () => {
  downloadCastHelper();
});

let noDevicesFoundMenuItem: IINA.MenuItem = null;
let scanningMenuItem: IINA.MenuItem = null;

function noDevicesFoundMenu() {
  if (noDevicesFoundMenuItem) return noDevicesFoundMenuItem;
  noDevicesFoundMenuItem = menu.item("Chromecast", () => {});
  noDevicesFoundMenuItem.addSubMenuItem(
    menu.item("No Devices Found", () => {}, {
      enabled: false,
    })
  );
  return noDevicesFoundMenuItem;
}

function scanningMenu() {
  if (scanningMenuItem) return scanningMenuItem;
  scanningMenuItem = menu.item("Chromecast", () => {});
  scanningMenuItem.addSubMenuItem(
    menu.item("Scanning...", () => {}, {
      enabled: false,
    })
  );
  return scanningMenuItem;
}

function devicesMenu(devices: DiscoverResult[]) {
  let devicesMenuItem = menu.item("Chromecast", () => {});
  for (let i = 0; i < devices.length; i++) {
    devicesMenuItem.addSubMenuItem(
      menu.item(devices[i].name, () => {
        castCurrent(devices[i].ip);
      })
    );
  }
  return devicesMenuItem;
}

IINAEvent.on("iina.window-loaded", () => {
  menu.removeAllItems();
  menu.addItem(scanningMenu());
  dynamicMenu();
  setInterval(dynamicMenu, 30000);
});

// Start scanning and updating menu items
async function dynamicMenu() {
  IINAConsole.log("Running dynamic menu");

  // Start searching for devices and do it every 30 seconds
  const devices = await discoverChromecasts();
  if (devices.length > 0) {
    menu.removeAllItems();
    menu.addItem(devicesMenu(devices));
  } else {
    menu.removeAllItems();
    menu.addItem(noDevicesFoundMenu());
  }
}

function castCurrent(device) {
  IINAConsole.log("Casting current media to ", device);
  global.postMessage("cast:load", {
    device,
    path: core.status.url.replace("file://", ""),
    contentType: "video/mp4",
    title: core.status.title || "IINA",
  });
  core.pause(); // stop decoding locally; IINA is now the remote
}

IINAEvent.on("mpv.pause.changed", () => {
  global.postMessage(mpv.getFlag("pause") ? "cast:pause" : "cast:play", {});
});

IINAEvent.on("mpv.seek", () => {
  global.postMessage("cast:seek", { time: mpv.getNumber("time-pos") });
});
