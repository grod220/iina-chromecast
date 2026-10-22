# Architecture and implementation constraints

`src/index.js` connects each IINA player to `src/controller.js` and one remote window. `src/global.js` owns detached helper startup/health checks. `src/remote-ui.js` is shared by the native window and the helper's HTTP remote. `src/activity.js` defines polling intervals; `src/subtitle-tracks.js` defines stable subtitle identity; `src/track-resume.js` preserves valid incoming audio/video resume choices after a cast.

The Go helper serves local media/WebVTT and implements Cast v2 control. Session ownership, liveness and reconnect are in `cast.go`, `connection.go` and `session.go`; scoped cancellation and parent-process shutdown are in `session_scope.go` and `process.go`. Compatibility planning and delivery recovery distinguish network failures from evidence of decoder rejection. Media/subtitle preparation runs cancellable FFmpeg jobs and caches derived files.

## Lifecycle boundaries

Restore outgoing local overrides in mpv's on_unload hook, before the next movie's watch-later settings are read. Mark incoming tracks ready in on_preloaded, after mpv's layout reset. Read the current file from mpv: IINA's cached URL can lag behind its live tracks. IINA's queued file-loaded notification is useful for refreshing status, but is not the authority for current-file readiness.

IINA can reuse an idle player core after its window closes. Give a reopened lifetime a fresh controller and owner identity. File/window generations invalidate obsolete asynchronous responses. Each cast has a separate ID; scoped Stop/controls must not operate on a replacement cast. Preserve cancellation even when Cast arrives after its abandonment request.

While casting, exclude transient aid/vid/volume/mute from that player's watch-later list before disabling local tracks. IINA can write resume files before window-will-close. Restore local playback first, then restore the exact original option list. Never disable resume globally as a workaround.

## IINA integration constraints

Keep the native menu static. Avoid synchronous OSD/overlay or native menu mutations from HTTP promise callbacks: IINA 1.5.0 has shown main-thread/JavaScriptCore deadlocks or stale native objects under those conditions. Publish status through the remote window. Install its message handlers after loadFile, which resets the message hub.

Detach the helper launcher so the long-running helper does not block IINA's serial plugin queue. Guard late callbacks after close, and keep one poll loop per current controller. Call preferences.sync() for durable writes. Plugin updates should use a fresh app launch; Reload All Plugins has crashed IINA while other installed plugins were reloading.

## Deliberate limits

Prepared conversion completes before playback; incremental HLS/fMP4 delivery is future work. Codec/model planning remains conservative and falls back based on delivered-media evidence. Exact custom-receiver capability queries and caption DOM rendering require a receiver application we control. The current Google Default Media Receiver controls native subtitle geometry; returned style fields do not prove that a TV visually rendered them.
