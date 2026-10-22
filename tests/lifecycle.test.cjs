const {test}=require('node:test');
const assert=require('node:assert/strict');
const {buildSync}=require('esbuild');
const vm=require('node:vm');
const bundled=buildSync({entryPoints:['src/controller.js'],bundle:true,format:'cjs',write:false,platform:'node'}).outputFiles[0].text;
const context={module:{exports:{}},exports:{}};vm.runInNewContext(bundled,context);
const {createController}=context.module.exports;
const settle=async()=>{for(let i=0;i<20;i++)await Promise.resolve();};
const deferred=()=>{let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve};};
function player({owner='window-a',server}={}){
 const core={status:{url:'file:///a.mkv',title:'A'},osd:()=>{}};
 const props={vid:'1',aid:'1',sid:'1',pause:true,volume:65,mute:false,'time-pos':37,'keep-open':'yes','sub-visibility':true,'watch-later-options':['start','aid','vid','volume','mute','sid']};
 const tracks=[{type:'video',id:1,'ff-index':0},{type:'audio',id:1,'ff-index':1,selected:true},{type:'sub',id:1,'ff-index':2,codec:'subrip'}];
 const calls=[],writes=[],seeks=[];let saved=null;
 const service=server||{state:{phase:'idle',active:false}};
 let gate=null;
 const api=async(method,path,data)=>{
  calls.push({method,path,data});
  if(method==='GET'){
   const snapshot={...service.state};const wait=gate;gate=null;if(wait)await wait.promise;return snapshot;
  }
  if(path==='/seek'&&service.controlGate)await service.controlGate.promise;
  if(path==='/cast'){service.state={phase:'connected',active:true,sourcePath:data.path,ownerId:data.ownerId,castId:data.castId,playerState:data.autoplay?'PLAYING':'PAUSED',currentTime:data.startTime,duration:120,volume:.2,muted:true};return {ok:true};}
  if(path==='/stop'){if(service.stopGate)await service.stopGate.promise;service.state={...service.state,phase:'idle',active:false};}
  return {...service.state};
 };
 const mpv={getString:p=>String(props[p]??''),getNumber:p=>Number(props[p]),getFlag:p=>Boolean(props[p]),getNative:p=>p==='track-list'?tracks:props[p],
  set:(p,v)=>{writes.push({p,v});props[p]=v;},command:(name,args)=>{if(name==='seek'){seeks.push(Number(args[0]));props['time-pos']=Number(args[0]);}}};
 const ctrl=createController({mpv,core,api,ownerId:owner,loadSaved:()=>saved,saveSaved:v=>saved=v});
 return {ctrl,props,core,calls,writes,seeks,service,blockPoll:()=>gate=deferred()};
}
const device={ip:'192.0.2.1',name:'TV'};

test('Stop at the beginning restores zero, rather than the pre-cast position',async()=>{
 const t=player();await t.ctrl.start(device);await settle();t.service.state.currentTime=0;await t.ctrl.poll();await t.ctrl.action('stop');
 assert.equal(t.props['time-pos'],0);
});

test('initial LOADING and BUFFERING clocks do not overwrite the saved movie position',async()=>{
 const t=player();await t.ctrl.start(device);await settle();
 for(const playerState of ['LOADING','BUFFERING']){t.service.state={...t.service.state,playerState,currentTime:0};await t.ctrl.poll();assert.equal(t.props['time-pos'],37,playerState);}
});

test('a late poll from before a file reload cannot re-enter the abandoned cast',async()=>{
 const t=player();await t.ctrl.start(device);await settle();const gate=t.blockPoll();const poll=t.ctrl.poll();
 await t.ctrl.fileChanged();gate.resolve();await poll;await settle();
 assert.equal(t.ctrl.isRemote(),false);assert.notEqual(t.props.aid,'no');assert.notEqual(t.props.vid,'no');
});

test('a second window on the same movie does not disable its tracks or stop the first window cast on close',async()=>{
 const server={state:{phase:'idle',active:false}};const first=player({server});await first.ctrl.start(device);await settle();
 const second=player({owner:'window-b',server});await second.ctrl.poll();
 assert.equal(second.ctrl.isRemote(),false);assert.equal(second.props.aid,'1');assert.equal(second.props.vid,'1');
 await second.ctrl.close();assert.equal(server.state.active,true);
});

test('automatic file-switch and close cleanup identify the cast being abandoned',async()=>{
 for(const how of ['fileChanged','close']){
  const t=player();await t.ctrl.start(device);await settle();const start=t.calls.find(c=>c.path==='/cast');
  await t.ctrl[how]();const stop=t.calls.find(c=>c.path==='/stop');
  assert.ok(start.data.castId,'each cast needs an identity');
  assert.equal(stop.data.castId,start.data.castId);assert.equal(stop.data.ownerId,start.data.ownerId);assert.equal(stop.data.abandon,true);
 }
});

test('closed controllers ignore later file-started notifications without touching mpv',async()=>{
 const t=player();await t.ctrl.start(device);await settle();await t.ctrl.close();const writes=t.writes.length;
 t.core.status.url='file:///b.mkv';await t.ctrl.fileChanged();assert.equal(t.writes.length,writes);
});

test('a reused core restores local playback before its next file selects tracks',async()=>{
 const t=player();await t.ctrl.start(device);await settle();
 t.service.state={...t.service.state,volume:.1,muted:true};await t.ctrl.poll();
 await t.ctrl.close();t.ctrl.prepareForReuse();
 assert.equal(t.props.aid,'auto');assert.equal(t.props.vid,'auto');assert.equal(t.props.volume,65);assert.equal(t.props.mute,false);
 assert.deepEqual(Array.from(t.props['watch-later-options']),['start','aid','vid','volume','mute','sid']);
});

test('an owned failed cast can reconnect after restoring local playback',async()=>{
 const t=player();await t.ctrl.start(device);await settle();t.service.state={...t.service.state,phase:'error',active:false};await t.ctrl.poll();
 assert.equal(t.ctrl.isRemote(),false);t.service.state={...t.service.state,phase:'connected',active:true,currentTime:19};
 await t.ctrl.action('reconnect');await settle();assert.equal(t.ctrl.isRemote(),true);assert.equal(t.props['time-pos'],19);
});


test('a new Cast waits for file-switch Stop instead of being silently discarded',async()=>{
 const t=player();await t.ctrl.start(device);await settle();
 t.service.stopGate=deferred();t.core.status.url='file:///b.mkv';const stopping=t.ctrl.fileChanged();t.ctrl.tracksChanged();await settle();
 const starting=t.ctrl.start(device);await settle();
 t.service.stopGate.resolve();await Promise.all([stopping,starting]);await settle();
 const casts=t.calls.filter(c=>c.path==='/cast');assert.equal(casts.length,2);assert.equal(casts[1].data.path,'/b.mkv');assert.equal(t.ctrl.isRemote(),true);
});


test('closing during a queued file-switch cleanup still cancels the abandoned cast',async()=>{
 const t=player();await t.ctrl.start(device);await settle();t.service.controlGate=deferred();
 const pending=t.ctrl.action('seek',{time:42});await settle();t.core.status.url='file:///b.mkv';
 const switching=t.ctrl.fileChanged();await t.ctrl.close();
 assert.ok(t.calls.some(c=>c.path==='/stop'&&c.data.castId),'close discarded the only cancellation');
 t.service.controlGate.resolve();await Promise.all([pending,switching]);
});
