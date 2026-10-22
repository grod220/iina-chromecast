import {createTrackResume} from './track-resume.js';
import {pollInterval} from './activity.js';
import {createController} from './controller.js';
import {discoverChromecasts} from './discover.ts';
const {core,mpv,event,menu,standaloneWindow:remoteWindow,preferences,console:log}=iina;
const ownerSeed=Date.now()+'-'+Math.random().toString(36).slice(2);
let windowEpoch=0;
const trackResume=createTrackResume(mpv);
let unloaded=false;
const deviceKey=device=>device.id||device.ip+':'+(device.port||8009);
let devices=[],scanning=false,discoveryError='',started=false,alive=true,pollTimer,lastSeen=0,remoteVisible=false,lastPublished='',loopOwner=null;
let savedStyleKey='';
async function api(method,path,data){
 const opts={headers:{'Content-Type':'application/json'}};if(data!==undefined)opts.data=data;
 if(data?.castId){opts.headers['X-IINA-Owner']=data.ownerId;opts.headers['X-IINA-Cast']=data.castId;}
 const r=method==='GET'?await iina.http.get('http://127.0.0.1:9092'+path):await iina.http.post('http://127.0.0.1:9092'+path,opts);
 if(r.statusCode>=400)throw new Error(r.text||'Chromecast command failed');
 return r.data??JSON.parse(r.text);
}
function savePreference(key,value){preferences.set(key,value);preferences.sync();}
function render(state=controller.getState()){
 if(!alive)return;
 if(!remoteVisible)return;
 // The menu is static. Never update native menu objects from HTTP callbacks;
 // IINA can destroy them while a callback is still in flight during a reload.
 const payload={...state,devices:devices.map(d=>({...d,key:deviceKey(d)})),scanning,discoveryError,savedDevice:preferences.get('lastDevice')};
 const key=JSON.stringify(payload);if(key===lastPublished)return;lastPublished=key;
 remoteWindow.postMessage('state',payload);
}
function makeController(){return createController({mpv,core,api,onState:render,ownerId:ownerSeed+':'+(++windowEpoch),
 loadSaved:()=>preferences.get('localPlayback'),saveSaved:value=>savePreference('localPlayback',value),
 loadStyle:()=>preferences.get('subtitleAppearance'),saveStyle:value=>{const key=JSON.stringify(value);if(key!==savedStyleKey){savedStyleKey=key;savePreference('subtitleAppearance',value);}},
 loadSubtitlePreference:path=>(preferences.get('subtitleSelections')||{})[path],
 saveSubtitlePreference:(path,value)=>{
  const all=preferences.get('subtitleSelections')||{};delete all[path];all[path]=value;
  const keys=Object.keys(all);while(keys.length>200)delete all[keys.shift()];
  savePreference('subtitleSelections',all);
 }
});}
let controller=makeController();
function savedDevice(){
 const state=controller.getState();
 if(state.device&&state.canReconnect){
  const match=state.device.match(/^(.*):(\d+)$/),ip=match?match[1]:state.device;
  return {ip,port:match?Number(match[2]):8009,name:state.deviceName||'Saved TV',model:state.deviceModel||'',hostname:state.deviceHostname||'',id:'saved-'+ip,lastKnown:true};
 }
 const saved=preferences.get('lastDevice');return saved?{...saved,lastKnown:true}:null;
}
async function scan(){
 if(!alive||scanning)return;scanning=true;discoveryError='';render();
 try{
  const found=(await discoverChromecasts()).filter(d=>d.ip&&d.deviceType!=='audio'&&d.deviceType!=='group');
  if(!alive)return;
  const fallback=savedDevice();
  if(fallback&&!found.some(d=>d.ip===fallback.ip||d.name===fallback.name))found.push(fallback);
  if(found.length){devices=found;lastSeen=Date.now();}
  else if(Date.now()-lastSeen>120000)devices=[];
 }catch(error){discoveryError='Unable to look for TVs. Try Refresh.';log.log('Chromecast discovery: '+error);}
 finally{scanning=false;render();}
}
async function handleAction(message){
 if(!alive)return;
 const current=controller;
 const {name,data={}}=message;
 if(name==='refresh')return scan();
 if(name==='cast'){
  const device=devices.find(d=>deviceKey(d)===data.device);
  if(!device){discoveryError='Choose a TV, or refresh the list.';render();return;}
  savePreference('lastDevice',{ip:device.ip,port:device.port||8009,name:device.name,model:device.model,hostname:device.hostname,id:device.id});
  return controller.start(device);
 }
 if(name==='preview'){
  try{const result=await current.previewSubtitle(data.id);if(alive&&current===controller)remoteWindow.postMessage('preview',{token:data.token,...result});}
  catch(error){if(alive&&current===controller)remoteWindow.postMessage('preview',{token:data.token,error:String(error.message||error)});}
  return;
 }
 return controller.action(name,data);
}
async function pollLoop(){
 const current=controller;if(loopOwner===current||!alive)return;loopOwner=current;clearTimeout(pollTimer);
 try{await current.poll();}finally{
  if(loopOwner===current)loopOwner=null;
  if(alive&&controller===current){clearTimeout(pollTimer);pollTimer=setTimeout(pollLoop,pollInterval(current.getState(),remoteVisible));}
 }
}
function activity(visible){
 remoteVisible=!!visible;
 if(remoteVisible){lastPublished='';render();pollLoop();}
}
function start(){
 if(started)return;started=true;
 const saved=savedDevice();if(saved)devices=[saved];
 remoteWindow.loadFile('dist/remote.html');
 remoteWindow.setProperty({title:'Chromecast Remote',resizable:true});remoteWindow.setFrame(500,760);
 // loadFile resets the message hub, so handlers must be installed afterward.
 remoteWindow.onMessage('ready',()=>render());
 remoteWindow.onMessage('activity',message=>activity(message.visible));
 remoteWindow.onMessage('action',handleAction);
 menu.removeAllItems();menu.addItem(menu.item('Chromecast Remote…',()=>{remoteWindow.open();activity(true);if(!controller.getState().active)scan();}));
 pollLoop();
}
event.on('iina.window-loaded',start);
for(const p of ['pause','volume','mute','sid','sub-visibility','aid'])event.on('mpv.'+p+'.changed',()=>controller.nativeChange(p));
event.on('mpv.seek',()=>controller.nativeChange('time-pos'));
event.on('iina.window-will-close',()=>{alive=false;remoteVisible=false;clearTimeout(pollTimer);controller.close();remoteWindow.close();});
// Restore local options before mpv reads the NEXT movie's resume file.
// on_load is too late: watch-later options have already been applied then.
mpv.addHook('on_unload',50,()=>{
 unloaded=true;trackResume.unload(controller.hasLocalOverrides());
 if(alive)controller.fileChanged();else controller.prepareForReuse();
});
// IINA reuses idle player cores after a window closes. Each reopened window
// gets a fresh controller; callbacks from the closed lifetime stay inert.
mpv.addHook('on_load',50,()=>{
 trackResume.load();
 const reopened=!alive;
 if(reopened){controller.prepareForReuse();controller=makeController();alive=true;lastPublished='';}
 if(reopened||!unloaded)controller.fileChanged();
 unloaded=false;
});
mpv.addHook('on_preloaded',50,()=>{trackResume.preloaded();controller.tracksChanged();});
event.on('iina.file-started',()=>{if(alive)pollLoop();});

// IINA's queued file-loaded notification can belong to an intermediate file.
// Only mpv's synchronous on_preloaded hook marks the current tracks ready.
event.on('iina.file-loaded',()=>{controller.tracksChanged(false);if(alive)pollLoop();});
event.on('mpv.track-list.changed',()=>controller.tracksChanged(false));
