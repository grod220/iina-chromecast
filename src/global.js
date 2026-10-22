const {utils,http,console:log}=iina;
let starting=false;
async function ensureHelper(){if(starting)return;starting=true;let healthy=false;try{try{const r=await http.get('http://127.0.0.1:9092/status');if(r.statusCode===200){healthy=true;return;}}catch{}
 const bin=utils.resolvePath('@data/casthelper');
 // The short-lived shell releases IINA's serial plugin queue immediately.
 // The helper watches IINA's PID and exits when its owner is gone.
 await utils.exec('/bin/sh',['-c','"$1" -parent "$PPID" -control 127.0.0.1:9092 -media 0.0.0.0:8010 </dev/null >/dev/null 2>&1 &','casthelper',bin]);
 }catch(error){log.log('Chromecast helper: '+error);}finally{starting=false;setTimeout(ensureHelper,healthy?15000:1000);}}
ensureHelper();
