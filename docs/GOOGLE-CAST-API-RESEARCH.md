> Historical research from the 0.3.1 baseline. See [the changelog](../CHANGELOG.md) for changes implemented afterward.

# Google Cast API research for IINA Chromecast 0.3.1

Reviewed 22 October 2026 against official Google developer documentation. This is a read-only assessment: no plugin, receiver or TV changes were made.

The largest potential performance improvement remains serving prepared segments while conversion/remuxing continues. Google's newer receiver APIs also offer precise capability checks, playback diagnostics and optional caption rendering control, but these require a receiver application we control. There is no documented drop-in Google sender SDK for the current native macOS/Go architecture.

## Supported platform fit

Google documents sender SDKs for Android, iOS and Cast-supported web platforms. Web Sender supports Cast-enabled browsers on desktop operating systems and requires HTTPS; the docs do not list Go, native macOS or WKWebView as supported sender platforms. Consequently, putting the Web Sender script into IINA's existing WKWebView is not a supported replacement for the Go helper. Moving sender control into a Cast-supported browser would change the user experience. This conclusion is an inference from Google's supported-platform list, rather than a claim that every possible bridge is technically impossible. [SDK reference](https://developers.google.com/cast/docs/reference), [Web Sender setup](https://developers.google.com/cast/docs/web_sender)

The Default Media Receiver is hosted by Google and accepts media URLs without app registration. Its UI is not customizable. A Custom Web Receiver is our hosted HTML/JavaScript application using the receiver SDK and can intercept commands and handle custom messages. [Receiver overview](https://developers.google.com/cast/docs/web_receiver)

## What is actually new

The current release notes list Web Receiver **3.0.0156**, previewed 16 July 2026 and fully rolled out 9 September 2026. It uses Shaka Player **4.15.56** by default, makes Shaka the default for HLS, and adds `getDeviceInformation()`. The latter returns identifying information, not codec capability. Google rolled back several earlier releases for regressions; version recency alone is not evidence of better playback on this particular TV. These are documentation facts, not a measurement of the test receiver's installed runtime. [Release notes](https://developers.google.com/cast/docs/release-notes)

For a future custom receiver, use the normal stable CAF release and its supported defaults. Google's HLS migration guide advises removing an explicit `shakaVersion` override once the SDK default has caught up, allowing future fixes to arrive. It calls for testing actual stream/container/device variants. The Go sender cannot set receiver initialization options in Google's prebuilt Default Media Receiver. [HLS/Shaka migration](https://developers.google.com/cast/docs/web_receiver/shaka_migration)

The 19 October 2026 Android Sender 23.0.0 entry deprecates `PrecacheManager`; it is not a new prefetch facility to adopt. Recent iOS wake-on-LAN and Android Output Switcher/device-suggestion features likewise belong to those sender SDKs and supported devices, rather than new Go/macOS APIs. [Release notes](https://developers.google.com/cast/docs/release-notes)

## Practical opportunities

| Opportunity | Applicable to current setup? | Benefit and constraint |
| --- | --- | --- |
| Preserve standard media/session identity and distinguish delivery failure from incompatible encoding | Yes | More reliable control and targeted fallback without unnecessary full video conversion. |
| Honor `supportedMediaCommands` in receiver status | Yes | Expose only controls the active media supports; a documented route for future playback-rate or queue controls. Validate the real receiver before adding a feature. |
| Stream uncached output as HLS/fMP4 or DASH | Potentially, after implementation/testing | Earlier first picture and less dependence on a full-movie temporary MP4. Requires segmented media, CORS, bounded buffering, seeking/cancellation handling and subtitle tests. It does not inherently require a custom receiver for ordinary unencrypted supported media. |
| Ask the receiver about exact codec/display support | Custom receiver | Fewer guesses and avoidable conversions; cannot call the receiver JavaScript API directly from Go. |
| Tune playback buffering or use richer errors/stats | Custom receiver | Can measure stalls and startup, then tune against evidence. Old MPL settings do not all apply to Shaka. |
| Caption DOM/CSS rendering | Custom receiver | Could offer controlled rounded/padded captions without burning text into video; device-specific synchronization/alignment tests required. |
| Playlist preloading | Useful only if playlist support is added | Can reduce gaps between items, but competes with current playback for bandwidth. |

### Immediate protocol improvements

Google defines playback-instance `mediaSessionId`, request-correlated replies and spontaneous status. `ERROR` includes download failure, so it does not establish a need to re-encode. `supportedMediaCommands` flags pause, seek, track editing, playback rate and queue operations; the media transport maximum is 64 KiB. The documented metadata discriminator is `metadataType`, while local `buildMediaLoad` writes `metadata.type`. The status `playbackRate` can be zero independently of player state; local `remote.go` substitutes 1 for zero, so it should distinguish an absent rate from explicit zero. This is a source-level correctness issue, not a reproduced TV defect. [Media playback messages](https://developers.google.com/cast/docs/media/messages)

The existing native WebVTT track approach aligns with the documented Default Media Receiver text-track interface. Google says explicit audio/video track APIs require a Custom Receiver; replacing the current audio-remux path with `EDIT_TRACKS_INFO` is not a supported shortcut on the default receiver. Media plus caption resources need CORS. [Media tracks](https://developers.google.com/cast/docs/android_sender/media_tracks)

### Streaming before preparation finishes

Cast supports HLS, DASH and progressive media delivery, with codec and device limits. HEVC is explicitly unsupported in Transport Stream containers; an HEVC-preserving segmented design should consider fragmented MP4 rather than MPEG-TS and must be tested on the test receiver. This is a proposed architecture, not a claimed measured improvement. Keep the present direct/cached file path and HTTP range seeking for files that already work efficiently. [Supported media](https://developers.google.com/cast/docs/media), [Receiver streaming protocols](https://developers.google.com/cast/docs/media/streaming_protocols)

### Precise capabilities and diagnostics

Receiver-side `CastReceiverContext.canDisplayType(mimeType, codecs, width, height, framerate)` offers more specific checks than a model-name heuristic. `getDeviceCapabilities()` can report HDR/display/audio capabilities, but may return null before readiness or an empty object when the platform exposes none. A custom CAF receiver could report these results to the helper through custom messages and cache them conservatively. Confirmed playback should remain a practical validation. [Streaming protocol capability checks](https://developers.google.com/cast/docs/media/streaming_protocols), [CastReceiverContext](https://developers.google.com/cast/docs/reference/web_receiver/cast.framework.CastReceiverContext)

`PlaybackConfig` has buffering and network hooks plus `shakaConfig`, but several legacy options are explicitly not supported by Shaka. Google cautions that configuration overrides can cause playback issues. A custom receiver's events and errors are a better starting point than lowering every buffer setting. [PlaybackConfig](https://developers.google.com/cast/docs/reference/web_receiver/cast.framework.PlaybackConfig), [PlayerManager](https://developers.google.com/cast/docs/reference/web_receiver/cast.framework.PlayerManager)

### Captions and future features

`TextTrackStyle` includes per-text backing and caption-window fields, but declaring a rounded window still does not prove a specific receiver renders it. `PlaybackConfig.enableUITextDisplayer` enables Shaka's DOM text renderer; Google warns of potential text/audio/video sync issues, especially on older devices, and possible container alignment problems with custom UI. Treat a custom caption renderer as an optional compatibility-tested project; retain the user's native subtitle choice now. [TextTrackStyle](https://developers.google.com/cast/docs/reference/web_receiver/cast.framework.messages.TextTrackStyle), [PlaybackConfig](https://developers.google.com/cast/docs/reference/web_receiver/cast.framework.PlaybackConfig)

A support-gated speed control could use standard `SET_PLAYBACK_RATE` with positive rates, rather than a custom namespace. Queue `preloadTime` is a receiver hint, not a guarantee. Preloading is useful for episodes but consumes bandwidth and does not fix first playback of one movie. [Playback-rate request](https://developers.google.com/cast/docs/reference/web_receiver/cast.framework.messages.SetPlaybackRateRequestData), [QueueItem](https://developers.google.com/cast/docs/reference/web_receiver/cast.framework.messages.QueueItem), [Content preload](https://developers.google.com/cast/docs/web_receiver/core_features#content_preload)

## Cost of adopting a custom receiver

A Custom Receiver needs registration/application ID and a reachable hosted page; development devices must be registered for unpublished apps. Published receiver pages require HTTPS. Importantly, Google explicitly allows media loaded by that HTTPS receiver to remain HTTP, so local movie streaming need not become a cloud upload. This is a larger maintenance/deployment choice, not a prerequisite for the immediate reliability fixes. [Registration](https://developers.google.com/cast/docs/registration)

Cast Connect means implementing or integrating an Android TV application, associating its package with a Cast app ID, and enabling compatible sender launch behavior. It is not a switch that improves the existing stock receiver on an arbitrary TV. [Android TV overview](https://developers.google.com/cast/docs/android_tv_receiver), [Cast Connect core features](https://developers.google.com/cast/docs/android_tv_receiver/core_features)

## Recommendation

First implement the small VLC-supported reliability improvements in the existing helper: route the media URL through the TV's actual network interface, enforce session/content ownership, and use bounded liveness/deadlines plus targeted conversion fallback. Add Google-schema conformance and supported-command handling to that work. Next prototype segmented streaming for uncached preparation while keeping native WebVTT and current direct/cached delivery. Consider a Custom CAF Receiver only when measured startup/buffering gains, precise capabilities or improved caption design justify registration, hosting and receiver maintenance. No general SDK replacement is currently justified for the native IINA workflow.
