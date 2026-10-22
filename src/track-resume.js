// Disabling local tracks marks mpv's previous track layout as user-selected.
// On a different layout, mpv can reset the NEXT file's resumed track IDs after
// on_load. Preserve explicit next-file choices across that cast boundary.
export function createTrackResume(mpv){
 let pending=false,choices=[];
 return {
  unload(hadOverrides){pending=hadOverrides;choices=[];},
  load(){
   choices=[];if(!pending)return;pending=false;
   for(const [name,type] of [['aid','audio'],['vid','video']]){
    const value=mpv.getString('options/'+name);
    if(/^\d+$/.test(value)&&Number(value)>0)choices.push({name,type,value});
   }
  },
  preloaded(){
   const tracks=mpv.getNative('track-list')||[];
   for(const {name,type,value} of choices)if(tracks.some(t=>t.type===type&&Number(t.id)===Number(value)))mpv.set('file-local-options/'+name,value);
   choices=[];
  }
 };
}
