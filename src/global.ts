const { utils, http, global, console: IINAConsole } = iina;
const bin = `${utils.resolvePath("@data/casthelper")}`; // path inside the bundle

IINAConsole.log("Casthelper path:", bin);
const CONTROL = "http://127.0.0.1:9092";

// messages arrive from each player's main entry
global.onMessage("cast:load", (d) => {
  IINAConsole.log("Casting from global", d);
  http.post(`${CONTROL}/cast`, {
    data: d,
    headers: {
      "Content-Type": "application/json",
    },
  });
});
global.onMessage("cast:pause", () => http.post(`${CONTROL}/pause`, {}));
global.onMessage("cast:play", () => http.post(`${CONTROL}/play`, {}));
global.onMessage("cast:seek", (d) =>
  http.post(`${CONTROL}/seek`, {
    data: d,
    headers: {
      "Content-Type": "application/json",
    },
  })
);
global.onMessage("cast:stop", () => http.post(`${CONTROL}/stop`, {}));

IINAConsole.log("Starting casthelper");
utils.exec(
  bin,
  ["-control", "127.0.0.1:9092", "-media", "0.0.0.0:8010"],
  undefined,
  (out) => {
    if (out.includes("READY")) console.log("helper up");
  }
);
