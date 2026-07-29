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
  sidebar.loadFile("dist/ui/sidebar/index.html");
  sidebar.onMessage("startScan", async () => {
    iina.console.log("OnMessage running!");
    const results = await discoverChromecasts();
    iina.console.log("Got results");
    iina.console.log(results);
    iina.sidebar.postMessage("scanResults", results);
  });
  sidebar.onMessage("cast:current", (device) => {
    castCurrent(device);
  });
});

IINAEvent.on("iina.window-loaded", () => {
  menu.addItem(
    menu.item("Chromecast", () => {
      sidebar.show();
    })
  );
});

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
