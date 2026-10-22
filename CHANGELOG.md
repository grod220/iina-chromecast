# Changelog

These entries summarize locally developed versions imported into this fork. They do not imply that each version was published as a GitHub release.

## 0.4.3

- Use mpv's current file identity and media title when IINA's cached metadata lags during rapid switching.
- Restore subtitle preferences at mpv's on_preloaded boundary; stale IINA file-loaded notifications cannot prematurely enable casting.
- Gate native subtitle persistence on current-file readiness and preserve literal percent signs in filenames.
- Add regression coverage for stale file identities and load-boundary ordering. All 34 JavaScript tests and Go race checks pass.

## 0.4.2

- Create a fresh controller when IINA reuses a closed movie window/core.
- Ignore obsolete polls; scope cleanup and commands to their window/cast; reject late abandoned Cast requests.
- Preserve confirmed playback positions through initial buffering/loading, including an actual zero position.
- Restore local overrides before the next movie reads resume settings and preserve valid incoming track choices across layout resets.
- Retry unhealthy helper startup promptly and bound shutdown when a receiver stops replying.
- Refresh text previews when reopened-file tracks arrive.

## 0.4.1

- Prevent transient disabled audio/video tracks and TV volume/mute from entering movie resume files when IINA saves before plugin shutdown callbacks.
- Restore the exact original watch-later option list after restoring local playback.

## 0.4.0

- Derive advertised media URLs from the TV socket's local interface.
- Check receiver, transport, content and media-session ownership; avoid controlling replacement playback.
- Add bounded writes, liveness detection and reconnect/reattach that preserves owned playback.
- Distinguish media delivery problems from codec rejection and use bounded audio-only/video-only/combined fallback plans.
- Honor receiver-supported commands and correct metadata/loading/clock semantics.

## 0.3.1

- Batch uncached embedded subtitle extraction and share preparation work across consumers.
- Coalesce seek/volume requests, accumulate skips and prioritize Stop behind the in-flight command.
- Reduce idle/paused polling and discovery work; avoid unchanged UI updates.

## 0.3.0 and earlier local improvements

- Consolidate controls into a standalone Chromecast Remote window.
- Add native text subtitle conversion, live track/style changes, previews and durable selection identity.
- Add compatibility planning, prepared-media caching, progress and native IINA control synchronization.
- Preserve source WebVTT cue layout and metadata where conversion permits.
