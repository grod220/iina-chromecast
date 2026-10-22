const {test}=require('node:test');const assert=require('node:assert/strict');const {buildSync}=require('esbuild');const vm=require('node:vm');
const code=buildSync({entryPoints:['src/track-resume.js'],bundle:true,format:'cjs',write:false}).outputFiles[0].text;const context={module:{exports:{}},exports:{}};vm.runInNewContext(code,context);
test('the next file resumed track survives mpv layout reset after casting',()=>{
 const options={'options/aid':'2','options/vid':'1'},writes=[];
 const guard=context.module.exports.createTrackResume({getString:p=>options[p],getNative:()=>[{type:'audio',id:1},{type:'audio',id:2},{type:'video',id:1}],set:(p,v)=>writes.push([p,v])});
 guard.unload(true);guard.load();options['options/aid']='auto';guard.preloaded();
 assert.deepEqual(writes,[['file-local-options/aid','2'],['file-local-options/vid','1']]);
 writes.length=0;guard.unload(false);guard.load();guard.preloaded();assert.deepEqual(writes,[]);
});
test('track restoration skips auto and absent IDs',()=>{
 const writes=[];const guard=context.module.exports.createTrackResume({getString:p=>p==='options/aid'?'7':'auto',getNative:()=>[{type:'audio',id:1}],set:(...args)=>writes.push(args)});
 guard.unload(true);guard.load();guard.preloaded();assert.deepEqual(writes,[]);
});
