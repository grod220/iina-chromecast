import {pollInterval} from './activity.js';
const nodes=new Map();
const $=id=>{if(!nodes.has(id))nodes.set(id,document.getElementById(id));return nodes.get(id);};
const text=(id,value)=>{if($(id).textContent!==String(value))$(id).textContent=value;};
const value=(id,next)=>{if(String($(id).value)!==String(next))$(id).value=next;};
const disabled=(id,next)=>{if($(id).disabled!==!!next)$(id).disabled=!!next;};
const hidden=(id,next)=>{if($(id).hidden!==!!next)$(id).hidden=!!next;};
const native=typeof iina!=='undefined'&&typeof iina.postMessage==='function';
let state={},devicesKey='',tracksKey='',context='',draft='off',dirty=false,previewToken=0,previewLoading=false,dragging=false,selectedDevice='';
const fmt=value=>{const n=Math.max(0,Math.floor(Number(value)||0));return (n>=3600?Math.floor(n/3600)+':':'')+String(Math.floor(n/60)%60).padStart(n>=3600?2:1,'0')+':'+String(n%60).padStart(2,'0');};
const deviceKey=d=>d.key||d.id||d.ip+':'+(d.port||8009);

function showMessage(message){text('message',message||'');hidden('message',!message);}
function placeholder(text){const p=document.createElement('p');p.className='preview-placeholder';p.textContent=text;$('samples').replaceChildren(p);}
function requestPreview(){
 const token=++previewToken;
 if(draft==='off'){previewLoading=false;placeholder('Subtitles are off. Choose a track to preview its text.');disabled('refresh-preview',true);return;}
 const track=(state.availableSubtitles||[]).find(t=>String(t.id)===draft);
 if(!track){previewLoading=false;placeholder('Loading subtitle tracks…');disabled('refresh-preview',true);return;}
 if(!track?.supported){previewLoading=false;placeholder('This image-based track is unavailable for native text casting.');disabled('refresh-preview',true);return;}
 previewLoading=true;placeholder('Loading text sample…');disabled('refresh-preview',true);
 send('preview',{id:Number(draft),token});
}
function renderPreview(result){
 if(result.token!==previewToken)return;
 previewLoading=false;disabled('refresh-preview',draft==='off');
 if(result.error){placeholder(result.error);return;}
 if(!result.samples?.length){placeholder('No text samples were found in this track.');return;}
 const elements=result.samples.map(sample=>{
  const box=document.createElement('blockquote');box.className='sample';
  const time=document.createElement('time');time.textContent='Sample at '+fmt(sample.start);
  const caption=document.createElement('p');caption.textContent=sample.text;
  box.append(time,caption);return box;
 });
 if(elements.length>1){
  const more=document.createElement('details');more.className='additional-sample';
  const summary=document.createElement('summary');summary.textContent='Another sample';more.append(summary,...elements.slice(1));
  $('samples').replaceChildren(elements[0],more);
 }else $('samples').replaceChildren(...elements);
}
function render(s){
 state=s;
 const busy=['preparing','connecting','reconnecting'].includes(s.phase),connected=s.phase==='connected',ready=connected&&s.playerState!=='LOADING';
 const active=['preparing','connecting','reconnecting','connected'].includes(s.phase)&&s.active;
 const phase=connected?(s.playerState==='LOADING'?'Loading…':s.playerState==='PAUSED'?'Paused':s.playerState==='BUFFERING'?'Buffering…':'Playing'):({preparing:'Preparing…',connecting:'Connecting…',reconnecting:'Reconnecting…',error:'Disconnected',disconnected:'Disconnected',ended:'Finished','taken-over':'TV in use'}[s.phase]||'Ready');
 text('phase',phase);if($('phase').dataset.connected!==String(ready))$('phase').dataset.connected=String(ready);
 const devices=s.devices||[],dk=JSON.stringify([devices.map(d=>[deviceKey(d),d.name]),!devices.length&&!!s.scanning]);
 if(dk!==devicesKey){
  devicesKey=dk;$('tv').replaceChildren();
  for(const device of devices)$('tv').add(new Option(device.name,deviceKey(device)));
  if(!devices.length)$('tv').add(new Option(s.scanning?'Looking for TVs…':'No TVs found',''));
  if(!devices.some(d=>deviceKey(d)===selectedDevice)){
   const matched=devices.find(d=>d.ip+':'+(d.port||8009)===s.device)||devices.find(d=>s.savedDevice&&(d.ip===s.savedDevice.ip||d.name===s.savedDevice.name))||devices[0];
   selectedDevice=matched?deviceKey(matched):'';
  }
 }
 if($('tv').value!==selectedDevice)value('tv',selectedDevice);
 disabled('tv',busy||!devices.length);disabled('refresh',!!s.scanning);text('refresh',s.scanning?'Looking…':'Refresh');
 const device=devices.find(d=>deviceKey(d)===selectedDevice);
 const currentDevice=!!device&&(device.ip+':'+(device.port||8009)===s.device||device.name===s.deviceName);
 text('device-hint',s.discoveryError||(ready&&currentDevice?'Connected to '+s.deviceName:device?.lastKnown?'Saved TV. Refresh if it is unavailable.':devices.length?'Choose where to play your video.':s.scanning?'Looking for TVs on your network.':'Turn on your TV, connect to the same network, then refresh.'));
 const alreadyCasting=connected&&currentDevice&&s.sourcePath===s.localPath;
 hidden('cast',!!alreadyCasting);
 disabled('cast',busy||!device||!s.hasLocalFile);
 text('cast',busy?'Connecting…':!s.hasLocalFile?'Open a Video in IINA':active&&!currentDevice?'Cast to This TV':'Cast Video');
 hidden('reconnect',!s.canReconnect||!['error','disconnected'].includes(s.phase));disabled('reconnect',busy);
 text('playing-label',active?'ON '+(s.deviceName||'TV').toUpperCase():'VIDEO IN IINA');
 hidden('playback',!active);
 text('title',active?s.title:s.localTitle||'Open a video in IINA to get started.');if($('title').title!==$('title').textContent)$('title').title=$('title').textContent;
 if(!dragging){if(Number($('seek').max)!==Math.max(1,s.duration||1))$('seek').max=Math.max(1,s.duration||1);value('seek',s.currentTime||0);text('elapsed',fmt(s.currentTime));}
 text('duration',fmt(s.duration));text('toggle',s.playerState==='PLAYING'?'Pause':'Play');
 for(const id of ['seek','back','toggle','forward','volume','mute'])disabled(id,!ready||!!s.pending);
 if(s.capabilities?.seek===false)for(const id of ['seek','back','forward'])disabled(id,true);
 if(s.playerState==='PLAYING'&&s.capabilities?.pause===false)disabled('toggle',true);
 value('volume',Math.round((s.volume||0)*100));text('mute',s.muted?'Unmute':'Mute');
 const tracks=s.availableSubtitles||[],tk=JSON.stringify(tracks.map(t=>[t.id,t.name,t.supported])),nextContext=s.localPath||s.sourcePath||'';
 const actual=(s.remoteMode?s.subtitlesEnabled:s.localSubtitlesEnabled)?String(s.remoteMode?s.selectedSubtitle:s.localSelectedSubtitle):'off';
 let updatePreview=false;
 if(nextContext!==context){context=nextContext;dirty=false;draft=actual;tracksKey='';updatePreview=true;}
 if(tk!==tracksKey){
  updatePreview=true;
  tracksKey=tk;$('subtitles').replaceChildren(new Option('Off','off'));
  for(const track of tracks){const option=new Option(track.name+(track.supported?'':' · Unavailable'),String(track.id));option.disabled=!track.supported;$('subtitles').add(option);}
  if(draft!=='off'&&!tracks.some(t=>String(t.id)===draft)){draft=actual;dirty=false;updatePreview=true;}
 }
 if(draft===actual)dirty=false;
 if(!dirty&&draft!==actual){draft=actual;updatePreview=true;}
 if($('subtitles').value!==draft)value('subtitles',draft);
 disabled('subtitles',!tracks.length||busy);
 const draftTrack=tracks.find(t=>String(t.id)===draft);
 hidden('use-track',!dirty);disabled('use-track',busy||!!s.pending||(s.remoteMode&&s.capabilities?.editTracks===false)||(draft!=='off'&&!draftTrack?.supported));text('use-track',draft==='off'?'Turn Off':'Use Track');
 const actualTrack=tracks.find(t=>String(t.id)===actual);
 text('active-track',tracks.length?(s.remoteMode?'On TV: ':'Selected: ')+(actual==='off'?'Off':actualTrack?.name||'Subtitles'):'No text subtitle tracks are available for this video.');
 disabled('refresh-preview',previewLoading||draft==='off'||!draftTrack?.supported);
 for(const id of ['size','background'])disabled(id,!tracks.length||busy||!!s.pending||(s.remoteMode&&s.capabilities?.editTracks===false));
 const size=String(s.textTrackStyle?.fontScale||1);if($('size').value!==size)value('size',size);
 const color=s.textTrackStyle?.backgroundColor||'#00000099';if($('background').value!==color)value('background',color);
 hidden('stop',!s.active);disabled('stop',!!s.stopping);text('stop',s.stopping?'Stopping…':s.phase==='preparing'?'Cancel':'Stop Casting');
 hidden('progress',s.phase!=='preparing');value('progress',s.progress||0);
 text('format',active?s.format?.description||'':'');
 showMessage(s.error||'');
 if(updatePreview)requestPreview();
}
async function send(name,data={}){
 if(native){iina.postMessage('action',{name,data});return;}
 if(name==='refresh'){await poll();return;}
 if(name==='preview'){
  try{const r=await fetch('/subtitle-preview?id='+encodeURIComponent(data.id)+'&time='+Number(state.currentTime||0));if(!r.ok)throw new Error(await r.text());renderPreview({token:data.token,...await r.json()});}
  catch(error){renderPreview({token:data.token,error:error.message});}return;
 }
 let endpoint=name,body=data;
 if(name==='cast')endpoint='reconnect';
 if(name==='toggle')endpoint=state.playerState==='PLAYING'?'pause':'play';
 if(name==='skip'){endpoint='seek';body={time:Math.max(0,Number(state.currentTime||0)+Number(data.seconds))};}
 if(name==='subtitles'&&body.style)body={...body,style:{...state.textTrackStyle,...body.style}};
 try{const r=await fetch('/'+endpoint,{method:'POST',headers:{'Content-Type':'application/json',...(state.castId?{'X-IINA-Owner':state.ownerId,'X-IINA-Cast':state.castId}:{})},body:JSON.stringify(endpoint==='stop'?{...body,ownerId:state.ownerId,castId:state.castId,abandon:true}:body)});if(!r.ok)throw new Error(await r.text());await poll();}
 catch(error){showMessage(error.message);}
}
$('tv').onchange=()=>{selectedDevice=$('tv').value;render(state);};
$('refresh').onclick=()=>send('refresh');$('cast').onclick=()=>send('cast',{device:selectedDevice});$('reconnect').onclick=()=>send('reconnect');
$('toggle').onclick=()=>send('toggle');$('back').onclick=()=>send('skip',{seconds:-10});$('forward').onclick=()=>send('skip',{seconds:10});$('stop').onclick=()=>send('stop');
$('mute').onclick=()=>send('volume',{level:state.volume||0,muted:!state.muted});$('volume').onchange=()=>send('volume',{level:Number($('volume').value)/100,muted:state.muted||false});
$('seek').onpointerdown=()=>dragging=true;$('seek').oninput=()=>text('elapsed',fmt($('seek').value));$('seek').onchange=()=>{dragging=false;send('seek',{time:Number($('seek').value)});};$('seek').onpointercancel=()=>dragging=false;
$('subtitles').onchange=()=>{draft=$('subtitles').value;dirty=true;render(state);requestPreview();};
$('use-track').onclick=()=>send('subtitles',draft==='off'?{enabled:false}:{selectedSubtitle:Number(draft),enabled:true});
$('refresh-preview').onclick=requestPreview;
$('size').onchange=()=>send('subtitles',{style:{fontScale:Number($('size').value)}});
$('background').onchange=()=>{const color=$('background').value,clear=color==='#00000000';send('subtitles',{style:{backgroundColor:color,windowColor:'#00000000',windowType:'NONE',edgeType:clear?'OUTLINE':'NONE',edgeColor:clear?'#000000FF':'#00000000'}});};
let polling=false,pollTimer;
async function poll(){
 if(polling)return;polling=true;
 try{
  const r=await fetch('/status',{cache:'no-store'});if(!r.ok)throw new Error('Unable to reach Chromecast helper');const s=await r.json();
  const match=(s.device||'').match(/^(.*):(\d+)$/),ip=match?match[1]:s.device;
  render({...s,remoteMode:!!s.active,localPath:s.sourcePath,localTitle:s.title,hasLocalFile:!!s.canReconnect,localSelectedSubtitle:s.selectedSubtitle,localSubtitlesEnabled:s.subtitlesEnabled,availableSubtitles:(s.subtitles||[]).map(t=>({...t,supported:true})),devices:ip?[{key:s.device,ip,port:match?Number(match[2]):8009,name:s.deviceName||'TV',lastKnown:true}]:[]});
 }catch(error){showMessage(error.message);}finally{polling=false;if(!native){clearTimeout(pollTimer);pollTimer=setTimeout(poll,pollInterval(state,!document.hidden));}}
}
if(native){
 iina.onMessage('state',render);iina.onMessage('preview',renderPreview);iina.postMessage('ready',{});
 const activity=visible=>iina.postMessage('activity',{visible});
 window.addEventListener('focus',()=>activity(true));window.addEventListener('blur',()=>activity(false));
 document.addEventListener('visibilitychange',()=>activity(!document.hidden&&document.hasFocus()));
}else{
 poll();document.addEventListener('visibilitychange',()=>{if(!document.hidden)poll();});
}
