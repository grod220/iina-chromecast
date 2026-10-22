// Keep native playback controls synchronized while casting, but let idle
// players sleep. Opening/focusing the remote always requests a fresh snapshot.
export function pollInterval(state,visible){
 if(['preparing','connecting','reconnecting'].includes(state.phase))return visible?750:1500;
 if(state.active&&state.phase==='connected')return state.playerState==='PLAYING'?750:(visible?1000:3000);
 if(state.phase==='disconnected')return visible?2000:5000;
 return visible?3000:10000;
}
