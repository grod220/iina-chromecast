const {test}=require('node:test');const assert=require('node:assert/strict');const {buildSync}=require('esbuild');const vm=require('node:vm');
const built=buildSync({entryPoints:['src/activity.js'],bundle:true,format:'cjs',write:false,platform:'node'}).outputFiles[0].text;
const context={module:{exports:{}},exports:{}};vm.runInNewContext(built,context);const {pollInterval}=context.module.exports;
test('idle players sleep while active playback stays synchronized',()=>{
 const playing={phase:'connected',active:true,playerState:'PLAYING'},paused={...playing,playerState:'PAUSED'};
 assert.ok(pollInterval({phase:'idle'},false)>=10000);assert.ok(pollInterval({phase:'idle'},false)>pollInterval({phase:'idle'},true));
 assert.ok(pollInterval(playing,false)<=1000);assert.ok(pollInterval(paused,false)>pollInterval(playing,false));assert.ok(pollInterval(paused,true)<pollInterval(paused,false));
 assert.ok(pollInterval({phase:'reconnecting'},true)<=1000);
});

test('plugin does not scan in the background or republish unchanged paused state',async()=>{
 const script=buildSync({entryPoints:['src/index.js'],bundle:true,format:'iife',write:false,platform:'browser'}).outputFiles[0].text;
 const timers=new Map(),events={},messages={},published=[],prefs=new Map();let nextTimer=0,openRemote,scans=0,statusCalls=0;
 const props={sid:'1','sub-visibility':true,pause:true,vid:'1',aid:'1',volume:50,'time-pos':10};
 const state={phase:'idle',active:false,currentTime:10,playerState:'PAUSED'};
 const iina={core:{status:{url:'file:///movie.mkv',title:'Movie'}},console:{log:()=>{}},preferences:{get:k=>prefs.get(k),set:(k,v)=>prefs.set(k,v),sync:()=>{}},
  mpv:{getString:p=>String(props[p]??''),getNumber:p=>Number(props[p])||0,getFlag:p=>!!props[p],getNative:()=>[],set:(p,v)=>props[p]=v,addHook:()=>{}},
  event:{on:(name,fn)=>events[name]=fn},menu:{removeAllItems:()=>{},item:(_name,fn)=>{openRemote=fn;return{};},addItem:()=>{}},
  standaloneWindow:{loadFile:()=>{},setProperty:()=>{},setFrame:()=>{},open:()=>{},close:()=>{},onMessage:(name,fn)=>messages[name]=fn,postMessage:(name,value)=>published.push({name,value})},
  http:{get:async()=>{statusCalls++;return{statusCode:200,data:{...state}};}},utils:{exec:async()=>{scans++;return{stdout:''};}}};
 const sandbox={iina,setTimeout:(fn,delay)=>{const id=++nextTimer;timers.set(id,{fn,delay});return id;},clearTimeout:id=>timers.delete(id),setInterval:()=>{throw Error('unexpected permanent scan loop');}};
 vm.runInNewContext(script,sandbox);const settle=async()=>{for(let i=0;i<12;i++)await Promise.resolve();};
 events['iina.window-loaded']();await settle();assert.equal(scans,0);assert.equal(statusCalls,1);assert.equal([...timers.values()][0].delay,10000);
 openRemote();await settle();assert.equal(scans,1);assert.ok(published.length>0);
 const before=published.length;const timer=[...timers.values()][0];timers.clear();timer.fn();await settle();assert.equal(published.length,before);assert.equal(scans,1);
 messages.activity({visible:false});await settle();const hiddenTimer=[...timers.values()][0];timers.clear();hiddenTimer.fn();await settle();assert.equal([...timers.values()][0].delay,10000);
 events['iina.window-will-close']();await settle();assert.equal(timers.size,0);
});
