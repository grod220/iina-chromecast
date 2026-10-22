(() => {
  // src/track-resume.js
  function createTrackResume(mpv2) {
    let pending = false, choices = [];
    return {
      unload(hadOverrides) {
        pending = hadOverrides;
        choices = [];
      },
      load() {
        choices = [];
        if (!pending) return;
        pending = false;
        for (const [name, type] of [["aid", "audio"], ["vid", "video"]]) {
          const value = mpv2.getString("options/" + name);
          if (/^\d+$/.test(value) && Number(value) > 0) choices.push({ name, type, value });
        }
      },
      preloaded() {
        const tracks = mpv2.getNative("track-list") || [];
        for (const { name, type, value } of choices) if (tracks.some((t) => t.type === type && Number(t.id) === Number(value))) mpv2.set("file-local-options/" + name, value);
        choices = [];
      }
    };
  }

  // src/activity.js
  function pollInterval(state, visible) {
    if (["preparing", "connecting", "reconnecting"].includes(state.phase)) return visible ? 750 : 1500;
    if (state.active && state.phase === "connected") return state.playerState === "PLAYING" ? 750 : visible ? 1e3 : 3e3;
    if (state.phase === "disconnected") return visible ? 2e3 : 5e3;
    return visible ? 3e3 : 1e4;
  }

  // src/subtitle-tracks.js
  var languages = { eng: "en", spa: "es", ara: "ar", fre: "fr", fra: "fr", por: "pt", ger: "de", deu: "de", ita: "it", jpn: "ja", zho: "zh", chi: "zh", kor: "ko", rus: "ru", dut: "nl", nld: "nl" };
  var names = { en: "English", es: "Spanish", ar: "Arabic", fr: "French", pt: "Portuguese", de: "German", it: "Italian", ja: "Japanese", zh: "Chinese", ko: "Korean", ru: "Russian", nl: "Dutch", und: "Unknown language" };
  var textSubtitle = (codec) => ["", "subrip", "srt", "ass", "ssa", "webvtt", "mov_text", "text"].includes(codec || "");
  var subtitleKey = (track) => track.externalPath ? "external:" + track.externalPath : "stream:" + track.streamIndex;
  function subtitleLabel(track) {
    const language = languages[track.language] || track.language || "und";
    const base = names[language] || language;
    const title = (track.name || "").trim();
    let name = base;
    if (title && title.toLowerCase() !== base.toLowerCase() && title.toLowerCase() !== (track.language || "").toLowerCase() && title !== "Subtitles") name += " \u2014 " + title;
    if (track.sdh && !/sdh/i.test(title)) name += " \xB7 SDH";
    if (track.forced && !/forced/i.test(title)) name += " \xB7 Forced";
    if (track.externalPath) name += " \xB7 External";
    return name + " \xB7 Track " + track.id;
  }
  function subtitleSpecs(tracks, delay = 0) {
    return tracks.filter((t) => t.type === "sub").map((t) => {
      let externalPath = "";
      if (t.external) try {
        externalPath = decodeURIComponent((t["external-filename"] || "").replace(/^file:\/\//, ""));
      } catch {
        externalPath = t["external-filename"] || "";
      }
      return { id: t.id, streamIndex: t["ff-index"] ?? -1, codec: t.codec || "", externalPath, name: t.title || "", language: t.lang || "und", delay, sdh: !!t["hearing-impaired"], forced: !!t.forced };
    });
  }
  function subtitleChoices(specs) {
    return specs.map((s) => ({ id: s.id, key: subtitleKey(s), name: subtitleLabel(s), language: languages[s.language] || s.language, sdh: s.sdh, forced: s.forced, external: !!s.externalPath, codec: s.codec, supported: textSubtitle(s.codec) }));
  }

  // src/controller.js
  function createController({ mpv: mpv2, core: core2, api: api2, onState = () => {
  }, ownerId, loadSaved = () => null, saveSaved = () => {
  }, loadStyle = () => null, saveStyle = () => {
  }, loadSubtitlePreference = () => null, saveSubtitlePreference = () => {
  }, now = () => Date.now() }) {
    let state = { phase: "idle" }, saved = null, remote = false, pending = 0, polling = false, failures = 0, lastRequest = null, closed = false, restoredPath = "", commandError = "", fileReady = true;
    const expected = /* @__PURE__ */ new Map(), jobs = [];
    let running = null, draining = false, generation = 0, resumeOptions = null, castSequence = 0, castId = "", lastPosition = null;
    const string = (p) => mpv2.getString(p);
    const number = (p) => Number(mpv2.getNumber(p)) || 0;
    const flag = (p) => Boolean(mpv2.getFlag(p));
    const same = (a, b) => typeof a === "number" && typeof b === "number" ? Math.abs(a - b) < 0.12 : String(a) === String(b);
    function path() {
      const current = string("path");
      if (current && !current.startsWith("file://")) return current;
      const url = current || core2.status.url || "";
      try {
        return decodeURIComponent(url.replace(/^file:\/\//, ""));
      } catch {
        return url.replace(/^file:\/\//, "");
      }
    }
    const specs = () => subtitleSpecs(mpv2.getNative("track-list") || [], number("sub-delay"));
    function put(p, value) {
      const current = typeof value === "boolean" ? flag(p) : typeof value === "number" ? number(p) : string(p);
      if (same(current, value)) return;
      expected.set(p, { value, until: now() + 2e3 });
      mpv2.set(p, value);
    }
    function consume(p, value) {
      const e = expected.get(p);
      if (!e) return false;
      if (now() > e.until) {
        expected.delete(p);
        return false;
      }
      if (same(value, e.value)) {
        expected.delete(p);
        return true;
      }
      return false;
    }
    function restorePreferred() {
      if (!fileReady || remote || !path() || restoredPath === path()) return;
      const tracks = specs();
      if (!tracks.length) return;
      restoredPath = path();
      const preference = loadSubtitlePreference(path());
      const track = preference && tracks.find((t) => subtitleKey(t) === preference.key);
      if (track) {
        put("sid", String(track.id));
        put("sub-visibility", !!preference.enabled);
      }
    }
    function rememberSubtitle(id, enabled, file = path()) {
      const track = specs().find((t) => t.id === Number(id));
      if (track && file === path()) saveSubtitlePreference(file, { key: subtitleKey(track), enabled: !!enabled });
    }
    function view() {
      if (closed) return { ...state, pending, remoteMode: false };
      const choices = subtitleChoices(specs());
      if (state.sourcePath === path()) for (const choice of choices) {
        const metadata = (state.subtitles || []).find((t) => t.id === choice.id);
        if (metadata?.codec) Object.assign(choice, metadata, { supported: choice.supported });
      }
      return { ...state, error: commandError || state.error, textTrackStyle: remote ? state.textTrackStyle : loadStyle() || state.textTrackStyle, remoteMode: remote, pending, stopping: running?.endpoint === "/stop" || jobs.some((job) => job.endpoint === "/stop"), localPath: path(), localTitle: string("media-title") || core2.status.title || path().split("/").pop() || "", hasLocalFile: fileReady && !!path() && !/^https?:/.test(path()), availableSubtitles: choices, localSelectedSubtitle: Number(string("sid")) || 0, localSubtitlesEnabled: flag("sub-visibility") && Number(string("sid")) > 0 };
    }
    function notify() {
      if (!closed) onState(view());
    }
    function protectResume() {
      if (resumeOptions !== null) return;
      const original = mpv2.getNative("watch-later-options");
      if (!Array.isArray(original)) throw new Error("Unable to protect local playback resume settings.");
      const localOnly = /* @__PURE__ */ new Set(["vid", "aid", "volume", "mute"]);
      mpv2.set("watch-later-options", original.filter((option) => !localOnly.has(option)));
      resumeOptions = [...original];
    }
    function restoreResume() {
      if (resumeOptions === null) return;
      mpv2.set("watch-later-options", resumeOptions);
      resumeOptions = null;
    }
    function owns(next) {
      return next.sourcePath === path() && (!next.ownerId || next.ownerId === ownerId) && (!next.castId || next.castId === castId);
    }
    function ownScope() {
      if (lastRequest?.castId) return { ownerId, castId: lastRequest.castId };
      const stop = running?.endpoint === "/stop" ? running : jobs.find((job) => job.endpoint === "/stop");
      return stop && stop.data.ownerId === ownerId && stop.data.castId ? { ownerId, castId: stop.data.castId } : null;
    }
    function enter() {
      if (remote) return;
      protectResume();
      const stored = loadSaved();
      if (!saved && stored?.path === path() && (string("vid") === "no" && string("aid") === "no")) saved = stored;
      if (!saved) saved = { vid: string("vid"), aid: string("aid"), volume: number("volume"), mute: flag("mute"), pause: flag("pause"), position: number("time-pos"), keepOpen: string("keep-open") };
      saveSaved({ ...saved, path: path() });
      remote = true;
      put("keep-open", "yes");
      put("aid", "no");
      put("vid", "no");
    }
    function leave() {
      if (!remote || !saved) return;
      remote = false;
      castId = "";
      const original = saved;
      saved = null;
      saveSaved(null);
      put("pause", true);
      put("vid", original.vid || "auto");
      put("aid", original.aid || "auto");
      put("volume", original.volume);
      put("mute", original.mute);
      put("keep-open", original.keepOpen || "yes");
      restoreResume();
      const position = Number.isFinite(lastPosition) ? lastPosition : original.position;
      expected.set("time-pos", { value: position, until: now() + 2e3 });
      mpv2.command("seek", [String(position), "absolute+exact"]);
    }
    function apply(next) {
      if (closed) return;
      state = next;
      const matches = owns(state);
      if (matches && state.active) {
        enter();
        restoredPath = path();
        if (!pending && state.phase === "connected") {
          if (state.textTrackStyle) saveStyle(state.textTrackStyle);
          if (["PLAYING", "PAUSED"].includes(state.playerState) && Number.isFinite(state.currentTime)) {
            lastPosition = state.currentTime;
            put("time-pos", lastPosition);
          }
          put("pause", !["PLAYING", "BUFFERING"].includes(state.playerState));
          if (Number.isFinite(state.volume)) put("volume", Math.round(state.volume * 100));
          put("mute", !!state.muted);
          if (state.selectedSubtitle) put("sid", String(state.selectedSubtitle));
          put("sub-visibility", !!state.subtitlesEnabled);
        }
      } else {
        if (remote && (!state.active || !matches)) leave();
        restorePreferred();
      }
      notify();
    }
    async function poll() {
      if (closed || polling) return view();
      polling = true;
      const epoch = generation;
      try {
        const next = await api2("GET", "/status");
        if (!closed && epoch === generation) {
          failures = 0;
          apply(next);
        }
      } catch (e) {
        if (!closed && epoch === generation && ++failures >= 3) {
          state = { ...state, phase: "disconnected", active: false, error: "The Chromecast helper is unavailable. Choose Reconnect." };
          leave();
          notify();
        }
      } finally {
        polling = false;
      }
      return view();
    }
    function updatePending() {
      pending = jobs.length + (running ? 1 : 0);
      notify();
    }
    function discardQueued() {
      for (const job of jobs.splice(0)) for (const resolve of job.waiters) resolve(view());
    }
    async function drain() {
      if (draining || closed) return;
      draining = true;
      while (jobs.length && !closed) {
        const job = jobs.shift();
        running = job;
        updatePending();
        try {
          const result = await api2("POST", job.endpoint, job.data);
          const superseded = job.generation !== generation || jobs.some((next) => next.endpoint === job.endpoint && ["/seek", "/volume"].includes(job.endpoint));
          running = null;
          pending = jobs.length;
          if (!closed && !superseded) {
            job.onSuccess(result);
            if (result?.phase) apply(result);
          }
        } catch (error) {
          running = null;
          pending = jobs.length;
          if (!closed && job.generation === generation) {
            commandError = String(error.message || error);
            notify();
          }
        }
        for (const resolve of job.waiters) resolve(view());
      }
      draining = false;
      running = null;
      updatePending();
      if (!closed) await poll();
    }
    function command(endpoint, data = {}, onSuccess = () => {
    }) {
      if (closed) return Promise.resolve(view());
      const stop = jobs.find((job) => job.endpoint === "/stop") || (running?.endpoint === "/stop" ? running : null);
      if (stop) return endpoint === "/stop" ? new Promise((resolve) => stop.waiters.push(resolve)) : Promise.resolve(view());
      commandError = "";
      const scope = data.castId ? { ownerId: data.ownerId, castId: data.castId } : castId ? { ownerId, castId } : state.castId ? { ownerId: state.ownerId, castId: state.castId } : {};
      data = { ...data, ...scope, ...endpoint === "/stop" ? { abandon: true } : {} };
      if (endpoint === "/stop") {
        generation++;
        discardQueued();
      }
      return new Promise((resolve) => {
        const replaceable = ["/seek", "/volume"].includes(endpoint);
        let job;
        if (replaceable) for (let i = jobs.length - 1; i >= 0; i--) {
          if (!["/seek", "/volume"].includes(jobs[i].endpoint)) break;
          if (jobs[i].endpoint === endpoint) {
            job = jobs[i];
            break;
          }
        }
        if (job) {
          job.data = data;
          job.onSuccess = onSuccess;
          job.waiters.push(resolve);
        } else jobs.push({ endpoint, data, onSuccess, waiters: [resolve], generation });
        updatePending();
        Promise.resolve().then(drain);
      });
    }
    function seekPosition() {
      const queued = [...jobs].reverse().find((job) => job.endpoint === "/seek");
      return (queued?.data.time ?? (running?.endpoint === "/seek" ? running.data.time : state.currentTime)) || 0;
    }
    function requestFor(device) {
      restorePreferred();
      const tracks = mpv2.getNative("track-list") || [], sub = specs().find((t) => t.id === Number(string("sid"))), audio = tracks.find((t) => t.type === "audio" && t.selected);
      return { device: device.ip + (device.port ? ":" + device.port : ""), deviceName: device.name, deviceModel: device.model, deviceHostname: device.hostname, ownerId, castId, path: path(), title: string("media-title") || core2.status.title || "IINA", startTime: remote ? Number(state.currentTime) || 0 : number("time-pos"), autoplay: remote ? state.playerState === "PLAYING" : !flag("pause"), audioIndex: audio?.["ff-index"], selectedSubtitle: sub?.id || 0, subtitlesEnabled: !!sub && flag("sub-visibility"), textTrackStyle: loadStyle() || state.textTrackStyle, subtitles: specs() };
    }
    async function start2(device) {
      const intendedPath = path();
      const stop = running?.endpoint === "/stop" ? running : jobs.find((job) => job.endpoint === "/stop");
      if (stop) await new Promise((resolve) => stop.waiters.push(resolve));
      if (closed || !fileReady || path() !== intendedPath) return view();
      generation++;
      castId = ownerId + ":" + ++castSequence;
      lastRequest = requestFor(device);
      lastPosition = lastRequest.startTime;
      enter();
      put("pause", true);
      return command("/cast", lastRequest);
    }
    function selectSubtitle(data) {
      const file = path(), id = Number(data.selectedSubtitle ?? (remote ? state.selectedSubtitle : string("sid"))) || 0;
      const enabled = data.enabled ?? (remote ? state.subtitlesEnabled : flag("sub-visibility"));
      const track = specs().find((t) => t.id === id);
      if ((data.selectedSubtitle !== void 0 || enabled) && (!track || !textSubtitle(track.codec))) {
        core2.osd("This subtitle track cannot be cast as text.");
        return Promise.resolve(view());
      }
      const body = { ...data };
      if (body.style) body.style = { ...loadStyle() || state.textTrackStyle, ...body.style };
      if (!remote) {
        if (body.style) saveStyle(body.style);
        if (data.selectedSubtitle !== void 0 && track) put("sid", String(id));
        if (data.enabled !== void 0) put("sub-visibility", !!enabled);
        if (data.selectedSubtitle !== void 0 || data.enabled !== void 0) rememberSubtitle(id, enabled, file);
        notify();
        return Promise.resolve(view());
      }
      return command("/subtitles", body, () => {
        if (data.selectedSubtitle !== void 0 || data.enabled !== void 0) rememberSubtitle(id, enabled, file);
      });
    }
    function nativeChange(p) {
      if (closed) return;
      const value = ["pause", "mute", "sub-visibility"].includes(p) ? flag(p) : ["time-pos", "volume"].includes(p) ? number(p) : string(p);
      if (consume(p, value)) return;
      if (!remote) {
        if (fileReady && restoredPath === path() && ["sid", "sub-visibility"].includes(p)) rememberSubtitle(Number(string("sid")), flag("sub-visibility") && Number(string("sid")) > 0);
        notify();
        return;
      }
      if (p === "pause") return command(value ? "/pause" : "/play");
      if (p === "time-pos") return command("/seek", { time: value });
      if (p === "volume" || p === "mute") return command("/volume", { level: Math.max(0, Math.min(1, number("volume") / 100)), muted: flag("mute") });
      if (p === "sid" || p === "sub-visibility") {
        const sid = Number(string("sid"));
        return selectSubtitle({ selectedSubtitle: sid > 0 ? sid : state.selectedSubtitle, enabled: sid > 0 && flag("sub-visibility") });
      }
      if (p === "aid" && value !== "no" && lastRequest) {
        const track = (mpv2.getNative("track-list") || []).find((t) => t.type === "audio" && String(t.id) === value);
        if (track) {
          saved.aid = value;
          put("aid", "no");
          lastRequest = { ...lastRequest, audioIndex: track["ff-index"], startTime: state.currentTime, autoplay: state.playerState === "PLAYING", textTrackStyle: state.textTrackStyle };
          return command("/cast", lastRequest);
        }
      }
    }
    function action(name, data = {}) {
      if (name === "toggle") return command(state.playerState === "PLAYING" ? "/pause" : "/play");
      if (name === "seek") return command("/seek", { time: Math.max(0, Number(data.time)) });
      if (name === "skip") return command("/seek", { time: Math.max(0, Number(seekPosition()) + Number(data.seconds)) });
      if (name === "subtitles") return selectSubtitle(data);
      if (name === "volume") return command("/volume", data);
      if (name === "stop") return command("/stop");
      if (name === "reconnect") {
        if (path() === state.sourcePath && (!state.ownerId || state.ownerId === ownerId)) {
          castId = state.castId || castId;
          lastPosition = Number.isFinite(state.currentTime) ? state.currentTime : number("time-pos");
          enter();
        }
        return command("/reconnect");
      }
      return Promise.resolve(view());
    }
    function previewSubtitle(id) {
      const subtitle = specs().find((t) => t.id === Number(id));
      if (!subtitle || !textSubtitle(subtitle.codec)) return Promise.reject(new Error("This track is not available as text."));
      return api2("POST", "/subtitle-preview", { clientId: ownerId, path: path(), subtitle, time: remote ? Number(state.currentTime) || 0 : number("time-pos") });
    }
    function releaseForNewFile() {
      remote = false;
      const original = saved;
      saved = null;
      saveSaved(null);
      expected.clear();
      if (original) put("pause", true);
      if (original) {
        if (string("vid") === "no") put("vid", "auto");
        if (string("aid") === "no") put("aid", "auto");
        put("volume", original.volume);
        put("mute", original.mute);
        put("keep-open", original.keepOpen || "yes");
      }
      restoreResume();
    }
    function fileChanged() {
      if (closed) return;
      const scope = ownScope();
      generation++;
      discardQueued();
      castId = "";
      lastRequest = null;
      lastPosition = null;
      restoredPath = "";
      commandError = "";
      fileReady = false;
      releaseForNewFile();
      state = { phase: "idle", active: false };
      if (scope) return command("/stop", { ...scope, abandon: true });
      notify();
    }
    function prepareForReuse() {
      releaseForNewFile();
    }
    function tracksChanged(loaded = true) {
      if (!closed) {
        if (loaded) fileReady = true;
        restorePreferred();
        notify();
      }
    }
    async function close() {
      const scope = ownScope();
      closed = true;
      remote = false;
      generation++;
      discardQueued();
      if (scope) try {
        await api2("POST", "/stop", { ...scope, abandon: true });
      } catch {
      }
    }
    return { poll, start: start2, action, nativeChange, fileChanged, tracksChanged, previewSubtitle, prepareForReuse, getState: view, isRemote: () => remote, hasLocalOverrides: () => saved !== null, restore: leave, close };
  }

  // src/discover.ts
  var { utils, console } = iina;
  var DNS_SD = "/usr/bin/dns-sd";
  var SERVICE = "_googlecast._tcp";
  var TIMEOUT_SEC = 3;
  var CAP = {
    VIDEO_OUT: 1 << 0,
    // 1
    VIDEO_IN: 1 << 1,
    // 2
    AUDIO_OUT: 1 << 2,
    // 4
    AUDIO_IN: 1 << 3
    // 8
  };
  function decodeCapabilities(ca) {
    if (ca === void 0) return null;
    const n = parseInt(ca, 10);
    if (Number.isNaN(n)) return null;
    return {
      videoOut: (n & CAP.VIDEO_OUT) !== 0,
      videoIn: (n & CAP.VIDEO_IN) !== 0,
      audioOut: (n & CAP.AUDIO_OUT) !== 0,
      audioIn: (n & CAP.AUDIO_IN) !== 0,
      raw: n
    };
  }
  function classify(md, caps) {
    if (md && /group/i.test(md)) return "group";
    if (caps) return caps.videoOut ? "video" : "audio";
    return "unknown";
  }
  function unescapeDNSLabel(s) {
    return s.replace(/\\(\d{3})/g, (_, d) => String.fromCharCode(parseInt(d, 10))).replace(/\\(.)/g, "$1");
  }
  function parseZone(stdout) {
    const devices2 = {};
    for (const line of stdout.split("\n")) {
      let m = line.match(/^(\S+)\s+SRV\s+\d+\s+\d+\s+(\d+)\s+(\S+)/);
      if (m) {
        const d = devices2[m[1]] || (devices2[m[1]] = {});
        d.port = parseInt(m[2], 10);
        d.hostname = m[3].replace(/\.$/, "");
        d.instanceName = unescapeDNSLabel(m[1].split(new RegExp("(?<!\\\\)\\."))[0]);
        continue;
      }
      m = line.match(/^(\S+)\s+TXT\s+(.*)$/);
      if (m) {
        const d = devices2[m[1]] || (devices2[m[1]] = {});
        const txt = d.txt || (d.txt = {});
        const pairs = m[2].match(/"([^"]*)"/g) || [];
        for (const raw of pairs) {
          const kv = raw.slice(1, -1);
          const eq = kv.indexOf("=");
          if (eq === -1) {
            txt[kv] = "";
          } else {
            txt[kv.slice(0, eq)] = kv.slice(eq + 1);
          }
        }
      }
    }
    return devices2;
  }
  function parseIP(stdout) {
    for (const line of stdout.split("\n")) {
      if (!/\bAdd\b/.test(line)) continue;
      const m = line.match(/\b(\d{1,3}(?:\.\d{1,3}){3})\b/);
      if (m) return m[1];
    }
    return null;
  }
  async function resolveIP(hostname) {
    try {
      const { stdout } = await utils.exec(DNS_SD, [
        "-t",
        String(TIMEOUT_SEC),
        "-G",
        "v4",
        hostname
      ]);
      return parseIP(stdout);
    } catch (e) {
      console.log(`dns-sd -G failed for ${hostname}: ${e}`);
      return null;
    }
  }
  async function discoverChromecasts() {
    let zone;
    try {
      const { stdout } = await utils.exec(DNS_SD, [
        "-t",
        String(TIMEOUT_SEC),
        "-Z",
        SERVICE,
        "local"
      ]);
      zone = parseZone(stdout);
    } catch (e) {
      console.log(`dns-sd -Z failed: ${e}`);
      return [];
    }
    const entries = Object.values(zone).filter((d) => d.hostname);
    return Promise.all(
      entries.map(async (d) => {
        const txt = d.txt || {};
        const caps = decodeCapabilities(txt["ca"]);
        const st = parseInt(txt["st"] ?? "", 10);
        return {
          name: txt["fn"] || d.instanceName || d.hostname,
          hostname: d.hostname,
          ip: await resolveIP(d.hostname),
          port: d.port ?? 8009,
          id: txt["id"] ?? null,
          model: txt["md"] ?? null,
          deviceType: classify(txt["md"], caps),
          isActive: !Number.isNaN(st) && st !== 0,
          statusText: txt["rs"] ?? "",
          version: txt["ve"] ?? null,
          iconPath: txt["ic"] ?? null,
          capabilities: caps,
          txt
          // raw map: anything not modeled above is still here
        };
      })
    );
  }

  // src/index.js
  var { core, mpv, event, menu, standaloneWindow: remoteWindow, preferences, console: log } = iina;
  var ownerSeed = Date.now() + "-" + Math.random().toString(36).slice(2);
  var windowEpoch = 0;
  var trackResume = createTrackResume(mpv);
  var unloaded = false;
  var deviceKey = (device) => device.id || device.ip + ":" + (device.port || 8009);
  var devices = [];
  var scanning = false;
  var discoveryError = "";
  var started = false;
  var alive = true;
  var pollTimer;
  var lastSeen = 0;
  var remoteVisible = false;
  var lastPublished = "";
  var loopOwner = null;
  var savedStyleKey = "";
  async function api(method, path, data) {
    const opts = { headers: { "Content-Type": "application/json" } };
    if (data !== void 0) opts.data = data;
    if (data?.castId) {
      opts.headers["X-IINA-Owner"] = data.ownerId;
      opts.headers["X-IINA-Cast"] = data.castId;
    }
    const r = method === "GET" ? await iina.http.get("http://127.0.0.1:9092" + path) : await iina.http.post("http://127.0.0.1:9092" + path, opts);
    if (r.statusCode >= 400) throw new Error(r.text || "Chromecast command failed");
    return r.data ?? JSON.parse(r.text);
  }
  function savePreference(key, value) {
    preferences.set(key, value);
    preferences.sync();
  }
  function render(state = controller.getState()) {
    if (!alive) return;
    if (!remoteVisible) return;
    const payload = { ...state, devices: devices.map((d) => ({ ...d, key: deviceKey(d) })), scanning, discoveryError, savedDevice: preferences.get("lastDevice") };
    const key = JSON.stringify(payload);
    if (key === lastPublished) return;
    lastPublished = key;
    remoteWindow.postMessage("state", payload);
  }
  function makeController() {
    return createController({
      mpv,
      core,
      api,
      onState: render,
      ownerId: ownerSeed + ":" + ++windowEpoch,
      loadSaved: () => preferences.get("localPlayback"),
      saveSaved: (value) => savePreference("localPlayback", value),
      loadStyle: () => preferences.get("subtitleAppearance"),
      saveStyle: (value) => {
        const key = JSON.stringify(value);
        if (key !== savedStyleKey) {
          savedStyleKey = key;
          savePreference("subtitleAppearance", value);
        }
      },
      loadSubtitlePreference: (path) => (preferences.get("subtitleSelections") || {})[path],
      saveSubtitlePreference: (path, value) => {
        const all = preferences.get("subtitleSelections") || {};
        delete all[path];
        all[path] = value;
        const keys = Object.keys(all);
        while (keys.length > 200) delete all[keys.shift()];
        savePreference("subtitleSelections", all);
      }
    });
  }
  var controller = makeController();
  function savedDevice() {
    const state = controller.getState();
    if (state.device && state.canReconnect) {
      const match = state.device.match(/^(.*):(\d+)$/), ip = match ? match[1] : state.device;
      return { ip, port: match ? Number(match[2]) : 8009, name: state.deviceName || "Saved TV", model: state.deviceModel || "", hostname: state.deviceHostname || "", id: "saved-" + ip, lastKnown: true };
    }
    const saved = preferences.get("lastDevice");
    return saved ? { ...saved, lastKnown: true } : null;
  }
  async function scan() {
    if (!alive || scanning) return;
    scanning = true;
    discoveryError = "";
    render();
    try {
      const found = (await discoverChromecasts()).filter((d) => d.ip && d.deviceType !== "audio" && d.deviceType !== "group");
      if (!alive) return;
      const fallback = savedDevice();
      if (fallback && !found.some((d) => d.ip === fallback.ip || d.name === fallback.name)) found.push(fallback);
      if (found.length) {
        devices = found;
        lastSeen = Date.now();
      } else if (Date.now() - lastSeen > 12e4) devices = [];
    } catch (error) {
      discoveryError = "Unable to look for TVs. Try Refresh.";
      log.log("Chromecast discovery: " + error);
    } finally {
      scanning = false;
      render();
    }
  }
  async function handleAction(message) {
    if (!alive) return;
    const current = controller;
    const { name, data = {} } = message;
    if (name === "refresh") return scan();
    if (name === "cast") {
      const device = devices.find((d) => deviceKey(d) === data.device);
      if (!device) {
        discoveryError = "Choose a TV, or refresh the list.";
        render();
        return;
      }
      savePreference("lastDevice", { ip: device.ip, port: device.port || 8009, name: device.name, model: device.model, hostname: device.hostname, id: device.id });
      return controller.start(device);
    }
    if (name === "preview") {
      try {
        const result = await current.previewSubtitle(data.id);
        if (alive && current === controller) remoteWindow.postMessage("preview", { token: data.token, ...result });
      } catch (error) {
        if (alive && current === controller) remoteWindow.postMessage("preview", { token: data.token, error: String(error.message || error) });
      }
      return;
    }
    return controller.action(name, data);
  }
  async function pollLoop() {
    const current = controller;
    if (loopOwner === current || !alive) return;
    loopOwner = current;
    clearTimeout(pollTimer);
    try {
      await current.poll();
    } finally {
      if (loopOwner === current) loopOwner = null;
      if (alive && controller === current) {
        clearTimeout(pollTimer);
        pollTimer = setTimeout(pollLoop, pollInterval(current.getState(), remoteVisible));
      }
    }
  }
  function activity(visible) {
    remoteVisible = !!visible;
    if (remoteVisible) {
      lastPublished = "";
      render();
      pollLoop();
    }
  }
  function start() {
    if (started) return;
    started = true;
    const saved = savedDevice();
    if (saved) devices = [saved];
    remoteWindow.loadFile("dist/remote.html");
    remoteWindow.setProperty({ title: "Chromecast Remote", resizable: true });
    remoteWindow.setFrame(500, 760);
    remoteWindow.onMessage("ready", () => render());
    remoteWindow.onMessage("activity", (message) => activity(message.visible));
    remoteWindow.onMessage("action", handleAction);
    menu.removeAllItems();
    menu.addItem(menu.item("Chromecast Remote\u2026", () => {
      remoteWindow.open();
      activity(true);
      if (!controller.getState().active) scan();
    }));
    pollLoop();
  }
  event.on("iina.window-loaded", start);
  for (const p of ["pause", "volume", "mute", "sid", "sub-visibility", "aid"]) event.on("mpv." + p + ".changed", () => controller.nativeChange(p));
  event.on("mpv.seek", () => controller.nativeChange("time-pos"));
  event.on("iina.window-will-close", () => {
    alive = false;
    remoteVisible = false;
    clearTimeout(pollTimer);
    controller.close();
    remoteWindow.close();
  });
  mpv.addHook("on_unload", 50, () => {
    unloaded = true;
    trackResume.unload(controller.hasLocalOverrides());
    if (alive) controller.fileChanged();
    else controller.prepareForReuse();
  });
  mpv.addHook("on_load", 50, () => {
    trackResume.load();
    const reopened = !alive;
    if (reopened) {
      controller.prepareForReuse();
      controller = makeController();
      alive = true;
      lastPublished = "";
    }
    if (reopened || !unloaded) controller.fileChanged();
    unloaded = false;
  });
  mpv.addHook("on_preloaded", 50, () => {
    trackResume.preloaded();
    controller.tracksChanged();
  });
  event.on("iina.file-started", () => {
    if (alive) pollLoop();
  });
  event.on("iina.file-loaded", () => {
    controller.tracksChanged(false);
    if (alive) pollLoop();
  });
  event.on("mpv.track-list.changed", () => controller.tracksChanged(false));
})();
