const {test}=require('node:test');const assert=require('node:assert/strict');const {buildSync}=require('esbuild');const vm=require('node:vm');
const built=buildSync({entryPoints:['src/controller.js'],bundle:true,format:'cjs',write:false,platform:'node'}).outputFiles[0].text;const ctx={exports:{},module:{exports:{}}};vm.runInNewContext(built,ctx);const {createController}=ctx.module.exports;
const tick=()=>new Promise(r=>setTimeout(r,20));
function setup(preferences={}){let ctrl;const props={vid:'1',aid:'1',sid:'1',pause:false,volume:50,mute:false,'sub-visibility':true,'time-pos':10,'keep-open':'yes','sub-delay':0,'watch-later-options':['start','vid','aid','volume','mute','sid','speed']};const tracks=[{type:'video',id:1,'ff-index':0,selected:true},{type:'audio',id:1,'ff-index':1,selected:true},{type:'sub',id:1,'ff-index':2,codec:'subrip',selected:true,lang:'eng'},{type:'sub',id:2,'ff-index':3,codec:'subrip',lang:'spa'}];let state={phase:'idle',active:false,sourcePath:'/movie.mkv',ownerId:'test',currentTime:10,duration:120,playerState:'PAUSED',volume:.5,muted:false,selectedSubtitle:1,subtitlesEnabled:true,subtitles:[{id:1},{id:2}]};const calls=[];const mpv={getString:p=>String(props[p]??''),getNumber:p=>Number(props[p])||0,getFlag:p=>!!props[p],getNative:p=>p==='watch-later-options'?props[p]:tracks,command:(name,args)=>{if(name==='seek'){props['time-pos']=Number(args[0]);queueMicrotask(()=>ctrl.nativeChange('time-pos'));}},set:(p,v)=>{props[p]=v;if(['pause','volume','mute','sid','sub-visibility','aid','time-pos'].includes(p))queueMicrotask(()=>ctrl.nativeChange(p));}};const api=async(method,path,data)=>{if(method==='GET')return {...state};calls.push({path,data});if(path==='/cast'){state={...state,phase:'connected',active:true,playerState:data.autoplay?'PLAYING':'PAUSED',currentTime:data.startTime,selectedSubtitle:data.selectedSubtitle,subtitlesEnabled:data.subtitlesEnabled};return {ok:true};}if(path==='/play')state.playerState='PLAYING';if(path==='/pause')state.playerState='PAUSED';if(path==='/seek')state.currentTime=data.time;if(path==='/stop'){state.phase='idle';state.active=false;}if(path==='/subtitles'){if(data.style)state.textTrackStyle=data.style;if('enabled'in data)state.subtitlesEnabled=data.enabled;if('selectedSubtitle'in data)state.selectedSubtitle=data.selectedSubtitle;}return {...state};};ctrl=createController({mpv,core:{status:{url:'file:///movie.mkv',title:'Movie'},osd:()=>{}},api,ownerId:'test',loadStyle:()=>preferences.style,saveStyle:value=>preferences.style=value,loadSubtitlePreference:path=>preferences.selections?.[path],saveSubtitlePreference:(path,value)=>{preferences.selections??={};preferences.selections[path]=value;}});return {ctrl,props,calls,setState:s=>state={...state,...s},tracks};}
test('TV state drives native clock without feedback commands or local decoding',async()=>{const t=setup();await t.ctrl.start({ip:'1.2.3.4',port:8009,name:'TV',model:'SWTV'});await tick();assert.equal(t.props.vid,'no');assert.equal(t.props.aid,'no');assert.equal(t.props.pause,false);t.setState({currentTime:42,playerState:'PAUSED'});await t.ctrl.poll();await tick();assert.equal(t.props['time-pos'],42);assert.equal(t.props.pause,true);assert.deepEqual(t.calls.map(c=>c.path),['/cast']);t.props.pause=false;await t.ctrl.nativeChange('pause');await tick();assert.equal(t.calls.filter(c=>c.path==='/play').length,1);t.props['time-pos']=25;await t.ctrl.nativeChange('time-pos');await tick();assert.equal(t.calls.filter(c=>c.path==='/seek').length,1);assert.equal(t.props.aid,'no');await t.ctrl.action('stop');await tick();assert.equal(t.props.vid,'1');assert.equal(t.props.aid,'1');assert.equal(t.props.pause,true);});
test('native subtitle selection changes the TV without restarting playback',async()=>{const t=setup();await t.ctrl.start({ip:'1.2.3.4',name:'TV'});await tick();t.props.sid='2';await t.ctrl.nativeChange('sid');await tick();assert.equal(t.calls.at(-1).path,'/subtitles');assert.equal(t.calls.at(-1).data.selectedSubtitle,2);assert.equal(t.calls.filter(c=>c.path==='/cast').length,1);});

test('subtitle appearance survives a new controller and later casts',async()=>{const preferences={};const first=setup(preferences);await first.ctrl.start({ip:'1.2.3.4',name:'TV'});await first.ctrl.action('subtitles',{style:{fontScale:1.1,backgroundColor:'#00000000',windowColor:'#00000099',windowType:'ROUNDED_CORNERS',fontGenericFamily:'SANS_SERIF',edgeType:'NONE'}});assert.equal(preferences.style.fontScale,1.1);await first.ctrl.action('stop');const second=setup(preferences);await second.ctrl.poll();await second.ctrl.start({ip:'1.2.3.4',name:'TV'});const req=second.calls.find(c=>c.path==='/cast').data;assert.equal(req.textTrackStyle.fontScale,1.1);assert.equal(req.textTrackStyle.windowColor,'#00000099');});

test('saved subtitle choice survives track renumbering and remembers off',async()=>{
 const prefs={};const first=setup(prefs);await first.ctrl.poll();await first.ctrl.start({ip:'1.2.3.4',name:'TV'});
 await first.ctrl.action('subtitles',{selectedSubtitle:2,enabled:true});await first.ctrl.action('subtitles',{enabled:false});
 assert.equal(prefs.selections['/movie.mkv'].key,'stream:3');assert.equal(prefs.selections['/movie.mkv'].enabled,false);
 await first.ctrl.action('stop');const second=setup(prefs);second.tracks[3].id=7;await second.ctrl.poll();await tick();
 assert.equal(second.props.sid,'7');assert.equal(second.props['sub-visibility'],false);
 await second.ctrl.start({ip:'1.2.3.4',name:'TV'});const req=second.calls.find(c=>c.path==='/cast').data;
 assert.equal(req.selectedSubtitle,7);assert.equal(req.subtitlesEnabled,false);
});
test('previewing an unselected track does not select or reload it',async()=>{
 const t=setup();await t.ctrl.start({ip:'1.2.3.4',name:'TV'});await tick();await t.ctrl.previewSubtitle(2);
 assert.equal(t.calls.at(-1).path,'/subtitle-preview');assert.equal(t.calls.at(-1).data.subtitle.id,2);
 assert.equal(t.props.sid,'1');assert.equal(t.calls.filter(c=>c.path==='/cast').length,1);assert.equal(t.calls.filter(c=>c.path==='/subtitles').length,0);
});
test('track choices carry explicit SDH and forced flags',()=>{
 const t=setup();t.tracks[2]['hearing-impaired']=true;t.tracks[3].forced=true;
 const choices=t.ctrl.getState().availableSubtitles;assert.match(choices[0].name,/English.*SDH.*Track 1/);assert.match(choices[1].name,/Spanish.*Forced.*Track 2/);
});
test('late status responses cannot update a closed player',async()=>{
 let resolve,updates=0,writes=0;const ctrl=createController({mpv:{getString:()=>'',getNumber:()=>0,getFlag:()=>false,getNative:()=>[],set:()=>writes++},core:{status:{url:''},osd:()=>{}},api:()=>new Promise(r=>resolve=r),onState:()=>updates++});
 const pending=ctrl.poll();await ctrl.close();resolve({phase:'connected',active:true});await pending;
 assert.equal(updates,0);assert.equal(writes,0);
});

test('rapid seek and volume controls keep the latest queued values',async()=>{
 let release,blocked=false;const calls=[];let state={phase:'connected',active:true,sourcePath:'',playerState:'PAUSED',currentTime:10};
 const ctrl=createController({mpv:{getString:()=>'',getNumber:()=>0,getFlag:()=>false,getNative:()=>[],set:()=>{}},core:{status:{url:''}},api:async(method,path,data)=>{
  if(method==='GET')return state;calls.push({path,data});if(!blocked){blocked=true;await new Promise(r=>release=r);}if(path==='/seek')state={...state,currentTime:data.time};return state;
 }});
 await ctrl.poll();const first=ctrl.action('seek',{time:20});await Promise.resolve();
 const queued=[];for(let n=21;n<=100;n++){queued.push(ctrl.action('seek',{time:n}));queued.push(ctrl.action('volume',{level:n/100,muted:false}));}
 release();await Promise.all([first,...queued]);await tick();
 assert.deepEqual(calls.map(c=>c.path),['/seek','/seek','/volume']);assert.equal(calls[1].data.time,100);assert.equal(calls[2].data.level,1);assert.equal(ctrl.getState().pending,0);
});
test('Stop discards queued controls and runs immediately after the in-flight command',async()=>{
 let release,started=false;const calls=[];let state={phase:'connected',active:true,sourcePath:'',playerState:'PAUSED',currentTime:10};
 const ctrl=createController({mpv:{getString:()=>'',getNumber:()=>0,getFlag:()=>false,getNative:()=>[],set:()=>{}},core:{status:{url:''}},api:async(method,path,data)=>{
  if(method==='GET')return state;calls.push(path);if(!started){started=true;await new Promise(r=>release=r);}if(path==='/stop')state={...state,phase:'idle',active:false};return state;
 }});
 await ctrl.poll();const inFlight=ctrl.action('seek',{time:20});await Promise.resolve();
 const old=[ctrl.action('seek',{time:30}),ctrl.action('volume',{level:.7}),ctrl.action('subtitles',{style:{fontScale:1.1}})];
 const stop=ctrl.action('stop');await ctrl.action('volume',{level:.9});assert.equal(ctrl.getState().stopping,true);release();await Promise.all([inFlight,...old,stop]);await tick();
 assert.deepEqual(calls,['/seek','/stop']);assert.equal(ctrl.getState().pending,0);assert.equal(ctrl.getState().active,false);
});
test('repeated skip buttons accumulate while their requests are combined',async()=>{
 const t=setup();await t.ctrl.start({ip:'1.2.3.4',name:'TV'});await tick();
 await Promise.all([t.ctrl.action('skip',{seconds:10}),t.ctrl.action('skip',{seconds:10}),t.ctrl.action('skip',{seconds:10})]);await tick();
 const seeks=t.calls.filter(c=>c.path==='/seek');assert.equal(seeks.length,1);assert.equal(seeks[0].data.time,40);
});

test('file-loaded tracks restore the saved choice without waiting for an idle poll',()=>{
 const t=setup({selections:{'/movie.mkv':{key:'stream:3',enabled:true}}});
 t.ctrl.tracksChanged();assert.equal(t.props.sid,'2');assert.equal(t.calls.length,0);
});


test('closing while casting cannot save disabled tracks in the movie resume file',async()=>{
 const t=setup();const original=[...t.props['watch-later-options']];
 await t.ctrl.start({ip:'1.2.3.4',name:'TV'});await tick();
 t.setState({currentTime:42,playerState:'PAUSED'});await t.ctrl.poll();
 // IINA writes watch-later BEFORE window-will-close. Reproduce that order.
 const resume=Object.fromEntries(t.props['watch-later-options'].map(key=>[key,key==='start'?t.props['time-pos']:t.props[key]]));
 assert.ok(!('vid'in resume),'temporary vid=no poisons the next open');
 assert.ok(!('aid'in resume),'temporary aid=no poisons the next open');
 assert.ok(!('volume'in resume),'TV volume must not overwrite local resume volume');
 assert.ok(!('mute'in resume));assert.equal(resume.start,42);assert.equal(resume.sid,'1');
 await t.ctrl.close();
 assert.deepEqual(original,['start','vid','aid','volume','mute','sid','speed']);
});

test('Stop restores the exact user resume-option list after restoring local playback',async()=>{
 const t=setup();const original=['start','aid','vid','speed','sid','brightness'];t.props['watch-later-options']=[...original];
 await t.ctrl.start({ip:'1.2.3.4',name:'TV'});await tick();
 assert.deepEqual(Array.from(t.props['watch-later-options']),['start','speed','sid','brightness']);
 await t.ctrl.action('stop');await tick();
 assert.deepEqual(Array.from(t.props['watch-later-options']),original);assert.equal(t.props.aid,'1');assert.equal(t.props.vid,'1');
});
