# IINA Chromecast

A fork of [dpacmittal/iina-chromecast](https://github.com/dpacmittal/iina-chromecast), preserving the original history and author attribution, with a unified remote, native text subtitles, compatibility recovery and lifecycle fixes. Current implementation: **0.4.3**.

## Features

- One menu entry: **Plugin → Chromecast Remote…**. Select a TV, cast, control playback/volume, change subtitles, stop or reconnect from the same window.
- Native IINA play/pause, seek, volume and subtitle controls synchronize with the owned TV session. Local audio/video decoding is disabled while casting and restored when it ends.
- Embedded/external text subtitles, preserved WebVTT layout, live track/style changes, text previews and remembered per-movie choices.
- Direct playback where compatible; remuxing, audio conversion and video conversion when needed. Prepared outputs and subtitles are cached. Conversion currently finishes before playback starts.
- Bounded reconnection, media/session ownership checks, targeted compatibility fallback, stale-command protection and multi-window isolation.
- Coalesced seek/volume requests, batched subtitle extraction and reduced polling while idle.

Original media files are not edited. Native caption shape, padding and exact rendering depend on the receiver. Image-based subtitle tracks are not converted to native text captions.

## Requirements

- macOS and an IINA build with plugin support enabled. Live validation used IINA 1.5.0 with mpv 0.41.0.
- A Chromecast-compatible video receiver on the same reachable local network.
- Node.js 22 or later and npm, Go 1.23 or later, FFmpeg/FFprobe, and Xcode command-line tools for building/signing on macOS.

FFmpeg/FFprobe are found in PATH or common Homebrew locations, including `ffmpeg@8`. For example, install with `brew install ffmpeg@8`. The helper has no external Go dependencies.

## Build and verify

```sh
npm ci
npm test
npm run build:helper
npm run package
```

`build:helper` builds the web assets, runs the Go suite with the race detector, builds the current architecture and ad-hoc signs `build/casthelper`. `package` creates `build/iina-chromecast.iinaplugin`. To build a universal Apple Silicon/Intel helper instead, run `sh scripts/build-helper.sh --universal`.

The JavaScript entry points are bundled with the pinned esbuild version. `ui/` contains the HTML/CSS sources. The build also synchronizes the helper's embedded remote assets. Generated `dist/` and embedded remote assets are committed, following upstream's bundle distribution approach; CI checks for drift.

## Install a source build

Quit IINA and wait for its helper to exit. Back up any existing plugin bundle, helper and preferences first. This fork keeps the original plugin identifier, so it updates that installation rather than installing a second plugin alongside it.

After building:

```sh
IINA_PLUGIN_ROOT="$HOME/Library/Application Support/com.colliderli.iina/plugins"
IINA_CAST_DATA="$IINA_PLUGIN_ROOT/.data/com.dpacmittal.iina-chromecast"
mkdir -p "$IINA_CAST_DATA"
ditto build/iina-chromecast.iinaplugin "$IINA_PLUGIN_ROOT/com.dpacmittal.iina-chromecast.iinaplugin"
cp build/casthelper "$IINA_CAST_DATA/casthelper"
```

Enable plugins and IINA Chromecast in IINA, then open a movie and choose **Plugin → Chromecast Remote…**. macOS may require local-network access. Updating an existing installation does not require changing its saved subtitle preferences.

The helper runs its control listener at `127.0.0.1:9092` and publishes the active media/subtitle resources on LAN port `8010`. Its host address follows the TV connection's actual local interface. Stop Casting unpublishes those resources. The helper watches its owning IINA process and exits after that process ends.

This fork does not automatically download an executable from upstream. Use the helper built from the matching source version. Avoid **Reload All Plugins** during a cast; use a fresh app launch for plugin updates.

## Tests and maintenance notes

- `npm test`: controller, remote UI and real-entry-point lifecycle regression tests.
- `go -C go-casthelper test -race ./...`: protocol, HTTP, subtitle/media conversion, ownership, recovery and shutdown tests. FFmpeg-backed tests need FFmpeg installed.
- Hardware tests require explicit `IINA_CAST_LIVE_*` environment variables and can control the selected TV. They are skipped in ordinary test runs and CI. See [testing notes](docs/TESTING.md).

See [CHANGELOG.md](CHANGELOG.md), [architecture notes](docs/ARCHITECTURE.md), [live validation](docs/VALIDATION.md), and historical [VLC](docs/VLC-CASTING-RESEARCH.md) / [Google Cast](docs/GOOGLE-CAST-API-RESEARCH.md) research.

## Attribution and license status

The original plugin is by Deepak Mittal. This fork preserves its Git history and the upstream author information in `Info.json`. Upstream did not include a license file at the imported commit; this fork does not assign a new license to the upstream code. The research notes link to the primary sources that informed the changes; no VLC source files are included.
