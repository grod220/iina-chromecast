# casthelper

A zero-dependency Go helper for an IINA Chromecast plugin. It does the two jobs
IINA's JS plugin sandbox can't: serve the local file to the device over HTTP
(with Range support, so seeking works) and speak the Cast v2 protocol
(TLS + length-prefixed protobuf). The IINA plugin drives it over a localhost
control API. Discovery stays in the plugin via macOS's `dns-sd`.

## Build (macOS, universal, self-contained)

```sh
# one arch each, then stitch into a universal binary
GOOS=darwin GOARCH=arm64 go build -o casthelper-arm64 .
GOOS=darwin GOARCH=amd64 go build -o casthelper-amd64 .
lipo -create -output casthelper casthelper-arm64 casthelper-amd64

# ad-hoc sign is enough when the plugin is installed via "Install from GitHub"
# (git-cloned files aren't quarantined). Use Developer ID + notarization only if
# you want it robust against every install path.
codesign -s - --options runtime casthelper
```

Drop the signed `casthelper` inside your `.iinaplugin` bundle and commit it with
the executable bit set.

## Control API (localhost, driven by the plugin)

| Method | Path    | Body                                                   | Purpose                        |
|--------|---------|--------------------------------------------------------|--------------------------------|
| POST   | /cast   | `{device, path, contentType, title}`                   | connect, launch, LOAD          |
| POST   | /play   | –                                                      | resume                         |
| POST   | /pause  | –                                                      | pause                          |
| POST   | /seek   | `{time}` (seconds)                                      | seek                           |
| POST   | /stop   | –                                                      | stop + tear down the session   |

`device` is the `ip` or `ip:8009` you resolved with `dns-sd`. `path` is the
absolute local file path; the helper serves it and builds the media URL itself.
The process prints `READY` on stdout once both servers are listening.

## IINA glue (sketch)

`Info.json` needs `"file-system"` (to `exec` the helper) and `"network-request"`
(to call the control API with `iina.http`).

```js
// global.js — owns the helper for IINA's whole lifetime
const { utils, http, global, console } = iina;
const bin = `${utils.resolvePath("@data/casthelper")}`; // path inside the bundle
const CONTROL = "http://127.0.0.1:9092";

utils.exec(bin, ["-control", "127.0.0.1:9092", "-media", "0.0.0.0:8010"], undefined, (out) => {
  if (out.includes("READY")) console.log("helper up");
});

// messages arrive from each player's main entry
global.onMessage("cast:load", (d) => http.post(`${CONTROL}/cast`, { data: d }));
global.onMessage("cast:pause", () => http.post(`${CONTROL}/pause`, {}));
global.onMessage("cast:play",  () => http.post(`${CONTROL}/play`, {}));
global.onMessage("cast:seek",  (d) => http.post(`${CONTROL}/seek`, { data: d }));
global.onMessage("cast:stop",  () => http.post(`${CONTROL}/stop`, {}));
```

```js
// main.js — per player window; only it can see mpv state
const { core, event, mpv, global } = iina;

function castCurrent(device) {
  global.postMessage("cast:load", {
    device,
    path: core.status.url.replace("file://", ""),
    contentType: "video/mp4",
    title: core.status.title || "IINA",
  });
  core.pause(); // stop decoding locally; IINA is now the remote
}

event.on("mpv.pause.changed", () => {
  global.postMessage(mpv.getFlag("pause") ? "cast:pause" : "cast:play", {});
});
event.on("mpv.seek", () => {
  global.postMessage("cast:seek", { time: mpv.getNumber("time-pos") });
});
```

## Not handled here (by design)

- **Discovery**: run `dns-sd -B _googlecast._tcp` + resolve in the plugin.
- **Codec compatibility / transcoding**: the Default Media Receiver only
  direct-plays a limited set (H.264/AAC etc.). Incompatible files need remux or
  ffmpeg transcode into the media server's output stream.
- **Subtitles**: serve a WebVTT track from the media server and add it to the
  LOAD `media.tracks`, then send `EDIT_TRACKS_INFO`.
