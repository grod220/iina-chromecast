const {test}=require('node:test');const assert=require('node:assert/strict');const {buildSync}=require('esbuild');const vm=require('node:vm');
const script=buildSync({entryPoints:['src/index.js'],bundle:true,format:'iife',write:false,platform:'browser'}).outputFiles[0].text;
test('a reused IINA player window gets a fresh working controller and one poll loop',async()=>{
 const events={},hooks={},messages={},timers=new Map(),posts=[];let id=0,openRemote,statusCalls=0;
 const props={vid:'1',aid:'1',sid:'1',pause:true,volume:65,mute:false,'time-pos':37,'keep-open':'yes','watch-later-options':['start','aid','vid','volume','mute','sid']};
 const prefs=new Map([['lastDevice',{id:'tv',ip:'192.0.2.1',port:8009,name:'TV',model:'SWTV'}]]);
 let state={phase:'idle',active:false};
 const iina={core:{status:{url:'file:///a.mkv',title:'A'}},console:{log:()=>{}},preferences:{get:k=>prefs.get(k),set:(k,v)=>prefs.set(k,v),sync:()=>{}},
 mpv:{getString:p=>String(props[p]??''),getNumber:p=>Number(props[p]),getFlag:p=>Boolean(props[p]),getNative:p=>p==='watch-later-options'?props[p]:[],set:(p,v)=>props[p]=v,command:()=>{},addHook:(name,_priority,fn)=>hooks[name]=fn},
 event:{on:(name,fn)=>events[name]=fn},menu:{removeAllItems:()=>{},item:(_name,fn)=>{openRemote=fn;return{};},addItem:()=>{}},
 standaloneWindow:{loadFile:()=>{},setProperty:()=>{},setFrame:()=>{},open:()=>{},close:()=>{},onMessage:(name,fn)=>messages[name]=fn,postMessage:()=>{}},
 http:{get:async()=>{statusCalls++;return{statusCode:200,data:{...state}};},post:async(url,options)=>{posts.push({url,data:options.data});if(url.endsWith('/cast'))state={...options.data,sourcePath:options.data.path,phase:'connected',active:true,currentTime:37,playerState:'PAUSED'};if(url.endsWith('/stop'))state={...state,active:false,phase:'idle'};return{statusCode:200,data:state};}},utils:{exec:async()=>({stdout:''})}};
 vm.runInNewContext(script,{iina,setTimeout:(fn,delay)=>{timers.set(++id,{fn,delay});return id;},clearTimeout:n=>timers.delete(n)});
 const settle=async()=>{for(let n=0;n<30;n++)await Promise.resolve();};
 events['iina.window-loaded']();await settle();
 openRemote();await settle();await messages.action({name:'cast',data:{device:'tv'}});await settle();
 const first=posts.find(p=>p.url.endsWith('/cast'));assert.ok(first);
 events['iina.window-will-close']();await settle();assert.equal(timers.size,0);
 if(hooks.on_load)hooks.on_load();events['iina.file-started']();hooks.on_preloaded();events['iina.file-loaded']();await settle();
 openRemote();await settle();await messages.action({name:'cast',data:{device:'tv'}});await settle();
 const casts=posts.filter(p=>p.url.endsWith('/cast'));assert.equal(casts.length,2,'reused player lost its plugin controller');
 assert.notEqual(casts[0].data.ownerId,casts[1].data.ownerId,'closed window lifetime was reused');
 assert.equal(timers.size,1);assert.ok(statusCalls>2);
 // mpv restores the next file's watch-later options BEFORE on_load.
 iina.core.status.url='file:///b.mkv';if(hooks.on_unload)hooks.on_unload();
 props.aid='2';props.vid='1';props.volume=30;props.mute=true;
 if(hooks.on_load)hooks.on_load();events['iina.file-started']();hooks.on_preloaded();events['iina.file-loaded']();await settle();
 assert.equal(props.aid,'2','cast cleanup overwrote the new file audio selection');
 assert.equal(props.volume,30,'cast cleanup overwrote the new file resume volume');assert.equal(props.mute,true);
 // An old IINA notification must not mark the new file ready before mpv does.
 hooks.on_unload();iina.core.status.url='file:///c.mkv';props.path='/c.mkv';hooks.on_load();
 events['iina.file-loaded']();await settle();
 const beforePreloaded=posts.filter(p=>p.url.endsWith('/cast')).length;
 await messages.action({name:'cast',data:{device:'tv'}});await settle();
 assert.equal(posts.filter(p=>p.url.endsWith('/cast')).length,beforePreloaded,'stale file-loaded enabled casting before current tracks were ready');
 hooks.on_preloaded();await messages.action({name:'cast',data:{device:'tv'}});await settle();
 assert.equal(posts.filter(p=>p.url.endsWith('/cast')).length,beforePreloaded+1);


});
