# casthelper

Zero-dependency Go Cast v2 client and local media/subtitle host for IINA Chromecast. It speaks TLS/protobuf Cast messages, serves Range-capable media and WebVTT, prepares compatible media with FFmpeg, and handles bounded session recovery and lifecycle cleanup.

Build from the repository root with `npm run build:helper`, or `sh scripts/build-helper.sh --universal` for both macOS architectures. Build the UI before Go so the embedded HTTP remote matches the native remote. See the [root README](../README.md) for installation and [architecture notes](../docs/ARCHITECTURE.md) for ownership/lifecycle rules.

The control listener defaults to 127.0.0.1:9092; the LAN media listener defaults to port 8010. The IINA launcher supplies its PID so the helper exits with its owner. It rejects requests as soon as that parent is gone and bounds receiver cleanup on exit.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | /status | Current cast, ownership, playback, progress and capabilities |
| POST | /cast | Prepare and load a local file on the selected receiver |
| POST | /play, /pause, /seek, /volume | Owned-session playback controls |
| POST | /subtitles | Text track, visibility and appearance changes |
| POST | /subtitle-preview | Prepare/read a text sample without changing playback |
| POST | /reconnect | Reattach/recover owned playback |
| POST | /stop | Stop/unpublish the matching cast |

The native controller supplies ownerId/castId on Cast and Stop and X-IINA-Owner/X-IINA-Cast headers on controls. Abandonment retires a cast identity so a delayed Cast cannot revive it. Unscoped legacy requests exist for compatibility; new callers should send the scope. A media ERROR is not sufficient proof of codec incompatibility: recovery checks delivery before attempting distinct audio/video conversion plans.

Normal tests: `go -C go-casthelper test -race ./...`. Real-TV tests require explicit opt-in; see [testing](../docs/TESTING.md).
