const {test}=require('node:test');const assert=require('node:assert/strict');const {buildSync}=require('esbuild');const vm=require('node:vm');
const code=buildSync({entryPoints:['src/controller.js'],bundle:true,format:'cjs',write:false,platform:'node'}).outputFiles[0].text;
const context={module:{exports:{}},exports:{}};vm.runInNewContext(code,context);const {createController}=context.module.exports;
function player(current='/new.mkv'){
 const core={status:{url:'file:///old.mkv',title:'Old'},osd(){}};
 const props={path:current,sid:'2',aid:'1',vid:'1',pause:true,volume:100,mute:false,'sub-visibility':true,'watch-later-options':['start','aid','vid','sid']};
 const tracks=[{type:'video',id:1,'ff-index':0},{type:'audio',id:1,'ff-index':1,selected:true},{type:'sub',id:1,'ff-index':2,codec:'subrip'},{type:'sub',id:2,'ff-index':3,codec:'subrip'}];
 const prefs={'/old.mkv':{key:'stream:3',enabled:true},'/new.mkv':{key:'stream:2',enabled:true}},calls=[],saved=[];
 const mpv={getString:p=>String(props[p]??''),getNumber:p=>Number(props[p]),getFlag:p=>Boolean(props[p]),getNative:p=>p==='track-list'?tracks:props[p],set:(p,v)=>props[p]=v,command(){}};
 const controller=createController({mpv,core,ownerId:'window',api:async(method,path,data)=>{calls.push({method,path,data});return{phase:'idle',active:false};},loadSubtitlePreference:p=>prefs[p],saveSubtitlePreference:(p,v)=>saved.push({p,v})});
 return {controller,props,calls,saved};
}
test('incoming tracks use mpv file identity while IINA metadata still names the previous file',async()=>{
 const t=player();t.controller.fileChanged();t.controller.tracksChanged();
 assert.equal(t.props.sid,'1','previous movie preference was applied to incoming tracks');
 t.props.sid='2';t.controller.nativeChange('sid');assert.equal(t.saved.at(-1).p,'/new.mkv');
 await t.controller.start({ip:'192.0.2.1',name:'TV'});
 assert.equal(t.calls.find(c=>c.path==='/cast').data.path,'/new.mkv');
});
test('a raw mpv filename containing percent escapes is not URL-decoded',()=>{
 const t=player('/movie%20literal.mkv');assert.equal(t.controller.getState().localPath,'/movie%20literal.mkv');
});
