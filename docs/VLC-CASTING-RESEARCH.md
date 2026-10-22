> Historical research from the 0.3.1 baseline. See [the changelog](../CHANGELOG.md) for changes implemented afterward.

# What VLC's Chromecast implementation can teach this plugin

The most useful immediate lessons are better network-address selection, strict ownership of the active TV session, and bounded connection liveness. VLC also demonstrates streaming while media is produced, which could shorten uncached startup substantially but requires a larger delivery redesign. Its subtitle path is not a reason to replace the plugin's working native WebVTT subtitles.

This is a source review, not a benchmark or a claim that the proposed failure scenarios were reproduced on the test receiver. No plugin runtime code or playback state was changed for this research.

## Sources and version scope

Reviewed on 22 October 2026 against local plugin 0.3.1:

- **VLC 3.0 maintenance branch:** canonical VideoLAN commit [`d80c4667a32c33bb300bb6a43e0bda3713905e2b`](https://code.videolan.org/videolan/vlc/-/commit/d80c4667a32c33bb300bb6a43e0bda3713905e2b), committed 17 October 2026. This is a maintenance-branch snapshot, not a claim that this exact commit is in a shipped release.
- **VLC development master:** official VideoLAN GitHub commit [`68e73a7b9f07f5a95fa77cb4cc4cac554e22f76d`](https://github.com/videolan/vlc/commit/68e73a7b9f07f5a95fa77cb4cc4cac554e22f76d), committed 21 October 2026. Development code must not be presented as the shipping VLC implementation.

Both were resolved with an as-of-22-October commit query. The separate official `videolan/vlc-3.0` GitHub mirror reported a last push in July 2024, so the maintenance comparison uses the canonical VideoLAN repository API instead. Source snapshots were retained locally during the review; they are not part of this repository.

## Adopt first

### 1. Advertise the address used to reach the TV

**Observed in VLC:** after opening the TV's TLS connection, VLC reads that socket's local address and uses it in the HTTP URL. This is present in both [3.0 maintenance](https://code.videolan.org/videolan/vlc/-/blob/d80c4667a32c33bb300bb6a43e0bda3713905e2b/modules/stream_out/chromecast/chromecast_communication.cpp#L54-68) and [master](https://github.com/videolan/vlc/blob/68e73a7b9f07f5a95fa77cb4cc4cac554e22f76d/modules/stream_out/chromecast/chromecast_communication.cpp#L57-L69).

**Local gap:** `go-casthelper/main.go:25–41` selects an address once at helper startup using the route to `8.8.8.8`, independently of the selected TV. `resolveDevice()` refreshes the TV address but not the advertised media address.

**Proposal:** derive the HTTP media/subtitle host from the actual TV connection's local address for each cast/reconnect; retain an explicit manual override. This addresses a concrete class of VPN, Ethernet/Wi-Fi, and network-change failures where control reaches the TV but the TV receives an unreachable media URL. It is a relatively small change with useful reliability benefit; it does not promise higher playback frame rate.

### 2. Treat takeover, disconnection, and failure as different outcomes

**Observed in VLC:** the controller tracks distinct states including `TakenOver`, `LoadFailed`, `Dead`, `Loading`, and `Stopping`. It ignores statuses for another established media session; `INTERRUPTED` transitions to `TakenOver` without resetting the old media-session ID, preventing new-session status from being adopted. See [3.0 states](https://code.videolan.org/videolan/vlc/-/blob/d80c4667a32c33bb300bb6a43e0bda3713905e2b/modules/stream_out/chromecast/chromecast.h#L70-95) and [session filtering/takeover](https://code.videolan.org/videolan/vlc/-/blob/d80c4667a32c33bb300bb6a43e0bda3713905e2b/modules/stream_out/chromecast/chromecast_ctrl.cpp#L841-881). These mechanisms also appear in [master](https://github.com/videolan/vlc/blob/68e73a7b9f07f5a95fa77cb4cc4cac554e22f76d/modules/stream_out/chromecast/chromecast_ctrl.cpp#L904-L943).

**Local gap:** the plugin already treats `INTERRUPTED` and virtual-connection closure as terminal; it is not entirely missing takeover handling. However, `cast.go:327–328` overwrites the current media-session ID from every accepted `MEDIA_STATUS`, without source/content/session ownership filtering. `session.go:626–635` automatically reloads our media when recovery cannot attach to the expected URL, even if another stream is now active.

**Proposal:** bind ownership to the expected receiver transport, media session, and content URL; distinguish an empty receiver from another active stream. Stop controlling or auto-reclaiming a TV after takeover. On teardown, send STOP only to the session still owned by this plugin. This closes the gap where controls or recovery could act on someone else's replacement cast.

Master additionally has [handoff-specific teardown preservation](https://github.com/videolan/vlc/blob/68e73a7b9f07f5a95fa77cb4cc4cac554e22f76d/modules/stream_out/chromecast/chromecast_ctrl.cpp#L254-L258), used by [a newer client-handoff path](https://github.com/videolan/vlc/blob/68e73a7b9f07f5a95fa77cb4cc4cac554e22f76d/modules/stream_out/chromecast/cast.cpp#L754-L763). That is development-only context, not a feature to port wholesale.

### 3. Bound socket liveness and wake up recovery on real failure

**Observed in VLC:** the receive path has a bounded idle wait; after inactivity it sends a PING, tracks a retry budget, and marks the connection dead when no response arrives. PONG replenishes the budget. See [3.0 constants](https://code.videolan.org/videolan/vlc/-/blob/d80c4667a32c33bb300bb6a43e0bda3713905e2b/modules/stream_out/chromecast/chromecast_ctrl.cpp#L47-48), [PONG handling](https://code.videolan.org/videolan/vlc/-/blob/d80c4667a32c33bb300bb6a43e0bda3713905e2b/modules/stream_out/chromecast/chromecast_ctrl.cpp#L688-708), and [timeout handling](https://code.videolan.org/videolan/vlc/-/blob/d80c4667a32c33bb300bb6a43e0bda3713905e2b/modules/stream_out/chromecast/chromecast_ctrl.cpp#L983-1027).

**Local gap:** `cast.go:414–430` sends a PING every five seconds but does not track PONG receipt. The socket read/write operations have no established-connection deadlines. Request timeouts occur after `send()` returns, so a blocked write can outlive the intended request timeout while holding the write lock. The two-second monitor normally detects unanswered GET_STATUS requests, but cannot replace a write deadline.

**Proposal:** track last inbound activity/PONG, bound writes, and close the transport on confirmed liveness failure so pending requests and recovery wake promptly. Preserve healthy quiet/paused sessions and use tested thresholds rather than copying VLC's constants blindly. Also use received receiver-status events to signal launch readiness, with bounded fallback polling, instead of relying solely on the current 300 ms launch poll.

## Compatibility and startup improvements

**Selective fallback:** VLC keeps audio/video conversion decisions separate and advances through alternate combinations. Starting from no forced conversion, the state function can progress through video-only, audio-only, then both, with actual starting state affected by codec compatibility. It is not an audio-first algorithm. See [3.0 fallback](https://code.videolan.org/videolan/vlc/-/blob/d80c4667a32c33bb300bb6a43e0bda3713905e2b/modules/stream_out/chromecast/cast.cpp#L907-925) and [master equivalent](https://github.com/videolan/vlc/blob/68e73a7b9f07f5a95fa77cb4cc4cac554e22f76d/modules/stream_out/chromecast/cast.cpp#L926-L943).

The plugin already chooses direct/remux/audio-only/video conversion for initial compatibility. Its error fallback is coarser: `session.go:579–594` sets `ForceTranscode`, and `media.go:99–100` then disables copying both video and audio. Adopt a bounded, evidence-driven fallback ladder, distinguishing delivery failure from codec failure and remembering demonstrated receiver capabilities. This can avoid expensive unnecessary video conversion. Do not blindly copy VLC's codec rules: [its 3.0 code rejects multichannel AAC and uses a Chromecast-name suffix to reject HEVC](https://code.videolan.org/videolan/vlc/-/blob/d80c4667a32c33bb300bb6a43e0bda3713905e2b/modules/stream_out/chromecast/cast.cpp#L787-829), while this TV has already played the user's HEVC/AAC file successfully.

**Stream during preparation:** VLC uses live Matroska/WebM output, bounded FIFO buffering, producer pacing, and retained data for a replacement HTTP fetch. Its [master mux definitions](https://github.com/videolan/vlc/blob/68e73a7b9f07f5a95fa77cb4cc4cac554e22f76d/modules/stream_out/chromecast/cast.cpp#L180-L181), [buffer thresholds](https://github.com/videolan/vlc/blob/68e73a7b9f07f5a95fa77cb4cc4cac554e22f76d/modules/stream_out/chromecast/cast.cpp#L210-L214), [HTTP replay/consumption](https://github.com/videolan/vlc/blob/68e73a7b9f07f5a95fa77cb4cc4cac554e22f76d/modules/stream_out/chromecast/cast.cpp#L475-L565), and [producer pacing](https://github.com/videolan/vlc/blob/68e73a7b9f07f5a95fa77cb4cc4cac554e22f76d/modules/stream_out/chromecast/cast.cpp#L585-L628) illustrate the design. The 3.0 snapshot has the same general bounded-streaming pattern.

The plugin's `prepareMedia()` waits for a complete file and atomic rename before serving it. A streaming path could improve first playback of uncached files, especially conversions, but requires seek/restart behavior, bounded memory, cancellation, and real-TV validation. Retain the existing file/range path for direct and cached playback. Simply exposing a growing MP4 through `http.ServeFile` is not equivalent to VLC's streaming implementation. Do not copy its container choice or byte thresholds without receiver-specific tests.

## Keep what already works

- **Native WebVTT subtitles:** master has its `CC_ENABLE_SPU` definition inside `#if 0`; the conditional subtitle path forces video conversion and adds `soverlay`. See [disabled feature](https://github.com/videolan/vlc/blob/68e73a7b9f07f5a95fa77cb4cc4cac554e22f76d/modules/stream_out/chromecast/cast.cpp#L49-L52), [forced conversion](https://github.com/videolan/vlc/blob/68e73a7b9f07f5a95fa77cb4cc4cac554e22f76d/modules/stream_out/chromecast/cast.cpp#L1042-L1052), and [overlay](https://github.com/videolan/vlc/blob/68e73a7b9f07f5a95fa77cb4cc4cac554e22f76d/modules/stream_out/chromecast/cast.cpp#L1125-L1126). Neither reviewed path offers a better ready-to-adopt native caption design. Keep live WebVTT track/style switching and authored cue preservation; do not introduce subtitle burn-in against the user's preference.
- **Receiver-authoritative clock:** VLC [interpolates from receiver timestamps and refreshes status periodically](https://code.videolan.org/videolan/vlc/-/blob/d80c4667a32c33bb300bb6a43e0bda3713905e2b/modules/stream_out/chromecast/chromecast_ctrl.cpp#L1173-1194). The plugin already implements that principle in `remote.go:21–38`; it is not a missing optimization.
- **Protocol bounds and separate listeners:** the plugin already bounds incoming Cast frame sizes and uses a localhost-only control listener separate from LAN media delivery. Those should survive any redesign.

The recommended sequence is route-derived URLs first; session ownership and bounded connection liveness together next; then selective compatibility fallback. Prototype streaming separately and measure the uncached startup benefit before replacing the reliable prepared-file path.
