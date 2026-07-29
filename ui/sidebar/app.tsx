import React, { useState } from "react";

const App = () => {
  const [devices, setDevices] = useState([]);
  const startScan = () => {
    //iina.console.log("Test");
    // @ts-ignore
    iina.onMessage("scanResults", (results) => {
      setDevices(results);
    });
    // @ts-ignore
    iina.postMessage("startScan", []);
  };

  const startCast = (ip) => {
    // @ts-ignore
    iina.postMessage("cast:current", ip);
  };
  return (
    <div>
      <div>
        {devices.map((device) => (
          <div onClick={() => startCast(device.ip)}>{device.name}</div>
        ))}
      </div>
      <button onClick={startScan}>Start Scan</button>
    </div>
  );
};

export default App;
