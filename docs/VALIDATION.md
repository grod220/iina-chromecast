# Validation of imported 0.4.3 implementation

Validation was completed on 22 October 2026 using macOS arm64, IINA 1.5.0, mpv 0.41.0 and a Chromecast-compatible TV. Private movie paths, receiver addresses, preferences, raw traces and viewing history are intentionally excluded from this repository. Original media files were not edited.

## Automated checks

All 34 JavaScript tests and the full Go test suite with race detection passed. New regression tests were observed failing before their fixes. Coverage includes per-window ownership, obsolete responses, queued Stop/start/close ordering, reused player cores, incoming resume settings, stale file identity, readiness hooks, preview refresh, cancellation tombstones, orphan helpers and bounded shutdown. Real FFmpeg fixture tests cover text extraction and WebVTT layout preservation. Opt-in TV tests are separate from ordinary CI.

## Live checks

- Two visible native movie windows in one IINA process, with independent controls and the same movie loaded in both.
- Closing an unrelated/duplicate movie window without stopping the cast.
- Casting from a second window, restoring the former owner's local state, and closing that former owner without interrupting replacement playback.
- A separate two-IINA-process isolation test; results were not confused with two windows in one process.
- Native Close, Finder reopen, native Quit during casting, and immediate relaunch through orphan-helper shutdown.
- After the final fix, four rapid-switch bursts issued 174 load requests: 30 at 100 ms, 60 at 20 ms, 60 without intentional delay, and 24 at 10 ms starting during Preparing. The abandoned cast stayed stopped, final local tracks/resume settings were restored, the saved subtitle choice remained selected, the other window stayed independent, and recasting worked.
- Native Quit with two movie windows and one active cast produced a safe resume file and exited IINA/helper. A clean launch restored local playback at the saved position.
- Installed scripts/assets and the signed helper matched the clean build; temporary instrumentation and IPC were removed afterward.

The stress count measures requested loads; it does not claim every intermediate movie decoded before the next request. Normal IINA autoplay can start a new local movie during a burst.

Earlier live checks covered direct playback, remux, audio/video conversion, native controls, text-language/style/visibility changes, reattachment without replacing the media session, and refusing to control a foreign takeover.

## Limits

These results do not cover every timing interleaving, receiver, codec, VPN topology, force-kill or power-loss scenario. Protocol state is not proof of visual subtitle rendering: some receivers echo rounded caption-window fields without drawing their background. Receiver-specific padding/corners and translucent multiline overlaps remain native-renderer limitations.
