const {test}=require('node:test');const assert=require('node:assert/strict');const {buildSync}=require('esbuild');const vm=require('node:vm');
test('an unchanged paused snapshot does not write to the DOM again',()=>{
 const script=buildSync({entryPoints:['src/remote-ui.js'],bundle:true,format:'iife',write:false,platform:'browser'}).outputFiles[0].text;
 let writes=0;const nodes=new Map(),messages={};
 function node(){const props={textContent:'',value:'',disabled:false,hidden:false,title:'',max:'',dataset:new Proxy({},{set:(o,k,v)=>{writes++;o[k]=v;return true;}}),append:()=>writes++,replaceChildren:()=>writes++,add:()=>writes++};return new Proxy(props,{set:(o,k,v)=>{writes++;o[k]=v;return true;}});}
 const document={getElementById:id=>{if(!nodes.has(id))nodes.set(id,node());return nodes.get(id);},createElement:node,addEventListener:()=>{}};
 const iina={postMessage:()=>{},onMessage:(name,fn)=>messages[name]=fn};
 vm.runInNewContext(script,{document,iina,window:{addEventListener:()=>{}},Option:function(name,value){this.name=name;this.value=value;}});
 const state={phase:'connected',active:true,remoteMode:true,localPath:'/movie.mkv',sourcePath:'/movie.mkv',title:'Movie',device:'1.2.3.4:8009',deviceName:'TV',devices:[{ip:'1.2.3.4',port:8009,name:'TV'}],currentTime:10,duration:120,playerState:'PAUSED',volume:.5,selectedSubtitle:1,subtitlesEnabled:true,availableSubtitles:[{id:1,name:'English',supported:true}]};
 messages.state(state);writes=0;messages.state({...state});assert.equal(writes,0);
 messages.state({...state,pending:1});assert.equal(nodes.get('stop').disabled,false,'Stop must remain available during another command');
 messages.state({...state,stopping:true});assert.equal(nodes.get('stop').disabled,true);
 messages.state({...state,playerState:'PLAYING',capabilities:{seek:false,pause:false,editTracks:false}});
 for(const id of ['seek','back','forward','toggle','size','background'])assert.equal(nodes.get(id).disabled,true,id+' ignores receiver capabilities');
 messages.state({...state,phase:'taken-over',active:false,canReconnect:false});
 assert.equal(nodes.get('phase').textContent,'TV in use');assert.equal(nodes.get('reconnect').hidden,true);assert.equal(nodes.get('playback').hidden,true);
});
test('subtitle preview recovers when tracks arrive after a reopened file',()=>{
 const script=buildSync({entryPoints:['src/remote-ui.js'],bundle:true,format:'iife',write:false,platform:'browser'}).outputFiles[0].text;
 const nodes=new Map(),messages={},sent=[];
 function node(){return {textContent:'',value:'',disabled:false,hidden:false,title:'',max:'',dataset:{},append(){},replaceChildren(...children){this.children=children;},add(){}};}
 const document={getElementById:id=>{if(!nodes.has(id))nodes.set(id,node());return nodes.get(id);},createElement:node,addEventListener(){}};
 const iina={postMessage:(name,data)=>sent.push({name,data}),onMessage:(name,fn)=>messages[name]=fn};
 vm.runInNewContext(script,{document,iina,window:{addEventListener(){}},Option:function(name,value){this.name=name;this.value=value;}});
 const state={phase:'idle',localPath:'/reopened.mkv',hasLocalFile:true,localSelectedSubtitle:1,localSubtitlesEnabled:true,availableSubtitles:[]};
 messages.state(state);
 assert.doesNotMatch(nodes.get('samples').children[0].textContent,/image-based/,'unknown tracks must not be labelled as images');
 const loaded={...state,availableSubtitles:[{id:1,name:'English',supported:true}]};
 messages.state(loaded);
 const previews=()=>sent.filter(m=>m.name==='action'&&m.data.name==='preview');
 assert.equal(previews().length,1,'loaded tracks should replace the placeholder with a preview request');
 assert.equal(previews()[0].data.data.id,1);
 messages.state({...loaded});assert.equal(previews().length,1,'unchanged snapshots must not refetch samples');
});
