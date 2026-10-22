# Testing

Run normal tests without any IINA_CAST_LIVE_* variables:

```sh
npm ci
npm test
npm run build
go -C go-casthelper test -race ./...
```

Install FFmpeg/FFprobe first to exercise the generated-media tests. The Go suite has no third-party module dependencies. JavaScript tests use the real bundled controller/index/UI code with controlled IINA and network boundaries.

Live tests in `go-casthelper/live_connection_test.go` require explicit opt-in and can control a TV:

- IINA_CAST_LIVE_TV: receiver host:port.
- IINA_CAST_LIVE_CLIP: absolute path to a disposable test video, for takeover tests.
- IINA_CAST_LIVE_HELPER: helper binary with local-network access, for takeover tests.

Start an owned cast before running an individual TestLive test. Do not set these variables in ordinary CI. Live reconnect verifies that the media session, content and paused position survive; takeover verifies that the original sender leaves replacement playback alone.

For native IINA lifecycle validation, check actual Cmd+W/Cmd+Q and Finder reopen in addition to mpv commands. mpv stop/loadfile alone follows different native save/window paths. Test both one process with multiple windows and separate processes, and verify which setup was created. Use disposable fixtures with different audio/subtitle layouts and resume values. Rapid-switch checks should assert final file identity, local tracks, original watch-later options, subtitle selection, abandoned-cast inactivity and successful recasting—not merely absence of a crash.

Keep private traces, runtime data and personal media outside the repository. Temporary test hooks/IPC must be removed before shipping. The public validation summary records hardware checks; it is not a substitute for rerunning them after changes.
