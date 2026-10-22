import {subtitleSpecs,subtitleChoices,subtitleKey,textSubtitle} from './subtitle-tracks.js';

export function createController({mpv,core,api,onState=()=>{},ownerId,loadSaved=()=>null,saveSaved=()=>{},loadStyle=()=>null,saveStyle=()=>{},loadSubtitlePreference=()=>null,saveSubtitlePreference=()=>{},now=()=>Date.now()}) {
 let state={phase:'idle'},saved=null,remote=false,pending=0,polling=false,failures=0,lastRequest=null,closed=false,restoredPath='',commandError='',fileReady=true;
 const expected=new Map(),jobs=[];
 let running=null,draining=false,generation=0,resumeOptions=null,castSequence=0,castId='',lastPosition=null;
 const string=p=>mpv.getString(p);
 const number=p=>Number(mpv.getNumber(p))||0;
 const flag=p=>Boolean(mpv.getFlag(p));
 const same=(a,b)=>typeof a==='number'&&typeof b==='number'?Math.abs(a-b)<0.12:String(a)===String(b);
 function path(){
  // IINA's cached URL can lag behind mpv during rapid file switches. Pair
  // track IDs with the file actually loaded by mpv, and preserve raw '%' names.
  const current=string('path');if(current&&!current.startsWith('file://'))return current;
  const url=current||core.status.url||'';try{return decodeURIComponent(url.replace(/^file:\/\//,''));}catch{return url.replace(/^file:\/\//,'');}
 }
 const specs=()=>subtitleSpecs(mpv.getNative('track-list')||[],number('sub-delay'));
 function put(p,value){
  const current=typeof value==='boolean'?flag(p):typeof value==='number'?number(p):string(p);
  if(same(current,value))return;
  expected.set(p,{value,until:now()+2000});mpv.set(p,value);
 }
 function consume(p,value){
  const e=expected.get(p);if(!e)return false;
  if(now()>e.until){expected.delete(p);return false;}
  if(same(value,e.value)){expected.delete(p);return true;}return false;
 }
 function restorePreferred(){
  if(!fileReady||remote||!path()||restoredPath===path())return;
  const tracks=specs();if(!tracks.length)return;
  restoredPath=path();const preference=loadSubtitlePreference(path());
  const track=preference&&tracks.find(t=>subtitleKey(t)===preference.key);
  if(track){put('sid',String(track.id));put('sub-visibility',!!preference.enabled);}
 }
 function rememberSubtitle(id,enabled,file=path()){
  const track=specs().find(t=>t.id===Number(id));
  if(track&&file===path())saveSubtitlePreference(file,{key:subtitleKey(track),enabled:!!enabled});
 }
 function view(){
  if(closed)return {...state,pending,remoteMode:false};
  const choices=subtitleChoices(specs());
  if(state.sourcePath===path())for(const choice of choices){
   const metadata=(state.subtitles||[]).find(t=>t.id===choice.id);
   if(metadata?.codec)Object.assign(choice,metadata,{supported:choice.supported});
  }
  return {...state,error:commandError||state.error,textTrackStyle:remote?state.textTrackStyle:(loadStyle()||state.textTrackStyle),remoteMode:remote,pending,stopping:running?.endpoint==='/stop'||jobs.some(job=>job.endpoint==='/stop'),localPath:path(),localTitle:string('media-title')||core.status.title||path().split('/').pop()||'',hasLocalFile:fileReady&&!!path()&&!/^https?:/.test(path()),availableSubtitles:choices,localSelectedSubtitle:Number(string('sid'))||0,localSubtitlesEnabled:flag('sub-visibility')&&Number(string('sid'))>0};
 }
 function notify(){if(!closed)onState(view());}
 function protectResume(){
  if(resumeOptions!==null)return;
  const original=mpv.getNative('watch-later-options');
  if(!Array.isArray(original))throw new Error('Unable to protect local playback resume settings.');
  // IINA writes watch-later before window-will-close. Exclude transient TV
  // state before disabling local tracks, rather than relying on close cleanup.
  const localOnly=new Set(['vid','aid','volume','mute']);
  mpv.set('watch-later-options',original.filter(option=>!localOnly.has(option)));
  resumeOptions=[...original];
 }
 function restoreResume(){
  if(resumeOptions===null)return;
  mpv.set('watch-later-options',resumeOptions);resumeOptions=null;
 }
 function owns(next){return next.sourcePath===path()&&(!next.ownerId||next.ownerId===ownerId)&&(!next.castId||next.castId===castId);}
 function ownScope(){
  if(lastRequest?.castId)return {ownerId,castId:lastRequest.castId};
  const stop=running?.endpoint==='/stop'?running:jobs.find(job=>job.endpoint==='/stop');
  return stop&&stop.data.ownerId===ownerId&&stop.data.castId?{ownerId,castId:stop.data.castId}:null;
 }
 function enter(){
  if(remote)return;
  protectResume();
  const stored=loadSaved();
  if(!saved&&stored?.path===path()&&(string('vid')==='no'&&string('aid')==='no'))saved=stored;
  if(!saved)saved={vid:string('vid'),aid:string('aid'),volume:number('volume'),mute:flag('mute'),pause:flag('pause'),position:number('time-pos'),keepOpen:string('keep-open')};
  saveSaved({...saved,path:path()});remote=true;put('keep-open','yes');put('aid','no');put('vid','no');
 }
 function leave(){
  if(!remote||!saved)return;
  remote=false;castId='';const original=saved;saved=null;saveSaved(null);
  put('pause',true);put('vid',original.vid||'auto');put('aid',original.aid||'auto');put('volume',original.volume);put('mute',original.mute);put('keep-open',original.keepOpen||'yes');
  restoreResume();
  const position=Number.isFinite(lastPosition)?lastPosition:original.position;
  expected.set('time-pos',{value:position,until:now()+2000});mpv.command('seek',[String(position),'absolute+exact']);
 }
 function apply(next){
  if(closed)return;
  state=next;const matches=owns(state);
  if(matches&&state.active){
   enter();restoredPath=path();
   if(!pending&&state.phase==='connected'){
    if(state.textTrackStyle)saveStyle(state.textTrackStyle);
    if(['PLAYING','PAUSED'].includes(state.playerState)&&Number.isFinite(state.currentTime)){lastPosition=state.currentTime;put('time-pos',lastPosition);}
    put('pause',!['PLAYING','BUFFERING'].includes(state.playerState));
    if(Number.isFinite(state.volume))put('volume',Math.round(state.volume*100));
    put('mute',!!state.muted);if(state.selectedSubtitle)put('sid',String(state.selectedSubtitle));put('sub-visibility',!!state.subtitlesEnabled);
   }
  }else{
   if(remote&&(!state.active||!matches))leave();
   restorePreferred();
  }
  notify();
 }
 async function poll(){
  if(closed||polling)return view();polling=true;const epoch=generation;
  try{const next=await api('GET','/status');if(!closed&&epoch===generation){failures=0;apply(next);}}
  catch(e){if(!closed&&epoch===generation&&++failures>=3){state={...state,phase:'disconnected',active:false,error:'The Chromecast helper is unavailable. Choose Reconnect.'};leave();notify();}}
  finally{polling=false;}return view();
 }
 function updatePending(){pending=jobs.length+(running?1:0);notify();}
 function discardQueued(){for(const job of jobs.splice(0))for(const resolve of job.waiters)resolve(view());}
 async function drain(){
  if(draining||closed)return;draining=true;
  while(jobs.length&&!closed){
   const job=jobs.shift();running=job;updatePending();
   try{
    const result=await api('POST',job.endpoint,job.data);
    const superseded=job.generation!==generation||jobs.some(next=>next.endpoint===job.endpoint&&['/seek','/volume'].includes(job.endpoint));
    running=null;pending=jobs.length;
    if(!closed&&!superseded){job.onSuccess(result);if(result?.phase)apply(result);}
   }catch(error){
    running=null;pending=jobs.length;
    if(!closed&&job.generation===generation){commandError=String(error.message||error);notify();}
   }
   for(const resolve of job.waiters)resolve(view());
  }
  draining=false;running=null;updatePending();
  if(!closed)await poll();
 }
 function command(endpoint,data={},onSuccess=()=>{}){
  if(closed)return Promise.resolve(view());
  const stop=jobs.find(job=>job.endpoint==='/stop')||(running?.endpoint==='/stop'?running:null);
  if(stop)return endpoint==='/stop'?new Promise(resolve=>stop.waiters.push(resolve)):Promise.resolve(view());
  commandError='';
  const scope=data.castId?{ownerId:data.ownerId,castId:data.castId}:castId?{ownerId,castId}:state.castId?{ownerId:state.ownerId,castId:state.castId}:{};
  data={...data,...scope,...(endpoint==='/stop'?{abandon:true}:{})};
  if(endpoint==='/stop'){generation++;discardQueued();}
  return new Promise(resolve=>{
   const replaceable=['/seek','/volume'].includes(endpoint);
   // Replace a queued value within this run of controls; preserve ordering
   // across play/pause, subtitle changes, and other deliberate actions.
   let job;
   if(replaceable)for(let i=jobs.length-1;i>=0;i--){
    if(!['/seek','/volume'].includes(jobs[i].endpoint))break;
    if(jobs[i].endpoint===endpoint){job=jobs[i];break;}
   }
   if(job){job.data=data;job.onSuccess=onSuccess;job.waiters.push(resolve);}
   else jobs.push({endpoint,data,onSuccess,waiters:[resolve],generation});
   updatePending();Promise.resolve().then(drain);
  });
 }
 function seekPosition(){
  const queued=[...jobs].reverse().find(job=>job.endpoint==='/seek');
  return (queued?.data.time??(running?.endpoint==='/seek'?running.data.time:state.currentTime))||0;
 }
 function requestFor(device){
  restorePreferred();
  const tracks=mpv.getNative('track-list')||[],sub=specs().find(t=>t.id===Number(string('sid'))),audio=tracks.find(t=>t.type==='audio'&&t.selected);
  return {device:device.ip+(device.port?':'+device.port:''),deviceName:device.name,deviceModel:device.model,deviceHostname:device.hostname,ownerId,castId,path:path(),title:string('media-title')||core.status.title||'IINA',startTime:remote?Number(state.currentTime)||0:number('time-pos'),autoplay:remote?state.playerState==='PLAYING':!flag('pause'),audioIndex:audio?.['ff-index'],selectedSubtitle:sub?.id||0,subtitlesEnabled:!!sub&&flag('sub-visibility'),textTrackStyle:loadStyle()||state.textTrackStyle,subtitles:specs()};
 }
 async function start(device){
  const intendedPath=path();
  const stop=running?.endpoint==='/stop'?running:jobs.find(job=>job.endpoint==='/stop');
  if(stop)await new Promise(resolve=>stop.waiters.push(resolve));
  if(closed||!fileReady||path()!==intendedPath)return view();
  generation++;castId=ownerId+':'+(++castSequence);lastRequest=requestFor(device);lastPosition=lastRequest.startTime;enter();put('pause',true);return command('/cast',lastRequest);
 }
 function selectSubtitle(data){
  const file=path(),id=Number(data.selectedSubtitle??(remote?state.selectedSubtitle:string('sid')))||0;
  const enabled=data.enabled??(remote?state.subtitlesEnabled:flag('sub-visibility'));
  const track=specs().find(t=>t.id===id);
  if((data.selectedSubtitle!==undefined||enabled)&&(!track||!textSubtitle(track.codec))){core.osd('This subtitle track cannot be cast as text.');return Promise.resolve(view());}
  const body={...data};if(body.style)body.style={...(loadStyle()||state.textTrackStyle),...body.style};
  if(!remote){
   if(body.style)saveStyle(body.style);
   if(data.selectedSubtitle!==undefined&&track)put('sid',String(id));
   if(data.enabled!==undefined)put('sub-visibility',!!enabled);
   if(data.selectedSubtitle!==undefined||data.enabled!==undefined)rememberSubtitle(id,enabled,file);
   notify();return Promise.resolve(view());
  }
  return command('/subtitles',body,()=>{if(data.selectedSubtitle!==undefined||data.enabled!==undefined)rememberSubtitle(id,enabled,file);});
 }
 function nativeChange(p){
  if(closed)return;
  const value=['pause','mute','sub-visibility'].includes(p)?flag(p):['time-pos','volume'].includes(p)?number(p):string(p);
  if(consume(p,value))return;
  if(!remote){if(fileReady&&restoredPath===path()&&['sid','sub-visibility'].includes(p))rememberSubtitle(Number(string('sid')),flag('sub-visibility')&&Number(string('sid'))>0);notify();return;}
  if(p==='pause')return command(value?'/pause':'/play');
  if(p==='time-pos')return command('/seek',{time:value});
  if(p==='volume'||p==='mute')return command('/volume',{level:Math.max(0,Math.min(1,number('volume')/100)),muted:flag('mute')});
  if(p==='sid'||p==='sub-visibility'){
   const sid=Number(string('sid'));return selectSubtitle({selectedSubtitle:sid>0?sid:state.selectedSubtitle,enabled:sid>0&&flag('sub-visibility')});
  }
  if(p==='aid'&&value!=='no'&&lastRequest){
   const track=(mpv.getNative('track-list')||[]).find(t=>t.type==='audio'&&String(t.id)===value);
   if(track){saved.aid=value;put('aid','no');lastRequest={...lastRequest,audioIndex:track['ff-index'],startTime:state.currentTime,autoplay:state.playerState==='PLAYING',textTrackStyle:state.textTrackStyle};return command('/cast',lastRequest);}
  }
 }
 function action(name,data={}){
  if(name==='toggle')return command(state.playerState==='PLAYING'?'/pause':'/play');
  if(name==='seek')return command('/seek',{time:Math.max(0,Number(data.time))});
  if(name==='skip')return command('/seek',{time:Math.max(0,Number(seekPosition())+Number(data.seconds))});
  if(name==='subtitles')return selectSubtitle(data);
  if(name==='volume')return command('/volume',data);
  if(name==='stop')return command('/stop');
  if(name==='reconnect'){if(path()===state.sourcePath&&(!state.ownerId||state.ownerId===ownerId)){castId=state.castId||castId;lastPosition=Number.isFinite(state.currentTime)?state.currentTime:number('time-pos');enter();}return command('/reconnect');}
  return Promise.resolve(view());
 }
 function previewSubtitle(id){
  const subtitle=specs().find(t=>t.id===Number(id));
  if(!subtitle||!textSubtitle(subtitle.codec))return Promise.reject(new Error('This track is not available as text.'));
  return api('POST','/subtitle-preview',{clientId:ownerId,path:path(),subtitle,time:remote?Number(state.currentTime)||0:number('time-pos')});
 }
 function releaseForNewFile(){
  remote=false;const original=saved;saved=null;saveSaved(null);expected.clear();
  if(original)put('pause',true);
  if(original){
   if(string('vid')==='no')put('vid','auto');if(string('aid')==='no')put('aid','auto');
   put('volume',original.volume);put('mute',original.mute);put('keep-open',original.keepOpen||'yes');
  }
  restoreResume();
 }
 function fileChanged(){
  if(closed)return;
  const scope=ownScope();generation++;discardQueued();castId='';lastRequest=null;lastPosition=null;
  restoredPath='';commandError='';fileReady=false;releaseForNewFile();state={phase:'idle',active:false};
  if(scope)return command('/stop',{...scope,abandon:true});
  notify();
 }
 // Called only by on_load, when a reused mpv core is safe to configure again.
 function prepareForReuse(){releaseForNewFile();}
 function tracksChanged(loaded=true){if(!closed){if(loaded)fileReady=true;restorePreferred();notify();}}
 async function close(){const scope=ownScope();closed=true;remote=false;generation++;discardQueued();if(scope)try{await api('POST','/stop',{...scope,abandon:true});}catch{} }
 return {poll,start,action,nativeChange,fileChanged,tracksChanged,previewSubtitle,prepareForReuse,getState:view,isRemote:()=>remote,hasLocalOverrides:()=>saved!==null,restore:leave,close};
}
