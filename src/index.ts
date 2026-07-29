import { discoverChromecasts } from "./discover";

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
  let mainMenuItem = menu.item("Chromecast", () => {});
  menu.addItem(mainMenuItem);
  mainMenuItem.addSubMenuItem(
    menu.item("No devices found", () => {}, {
      enabled: false,
    })
  );
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
    let mainMenuItem = menu.item("Chromecast", () => {});
    mainMenuItem = menu.item("Chromecast", () => {});
    menu.addItem(mainMenuItem);
    for (let i = 0; i < devices.length; i++) {
      mainMenuItem.addSubMenuItem(
        menu.item(devices[i].name, () => {
          castCurrent(devices[i].ip);
        })
      );
    }
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
