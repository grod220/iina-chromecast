const {test}=require('node:test');const assert=require('node:assert/strict');const {buildSync}=require('esbuild');const vm=require('node:vm');
const script=buildSync({entryPoints:['src/global.js'],bundle:true,format:'iife',write:false,platform:'browser'}).outputFiles[0].text;
for(const healthy of [false,true])test('helper health uses '+(healthy?'slow checks while healthy':'fast retries during startup or a rapid app restart'),async()=>{
 const timers=[];let launches=0;
 const schedule=(fn,delay)=>{timers.push({fn,delay});return timers.length;};
 vm.runInNewContext(script,{iina:{http:{get:async()=>({statusCode:healthy?200:503})},utils:{resolvePath:()=>'/helper',exec:async()=>{launches++;}},console:{log:()=>{}}},setTimeout:schedule,setInterval:schedule});
 for(let n=0;n<20;n++)await Promise.resolve();
 assert.equal(launches,healthy?0:1);assert.ok(timers.length>0);
 if(healthy)assert.ok(timers[0].delay>=15000);else assert.ok(timers[0].delay<=1000,'startup outage waited for the normal 15-second health interval');
});
