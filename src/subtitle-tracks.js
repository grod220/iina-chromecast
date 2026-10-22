const languages={eng:'en',spa:'es',ara:'ar',fre:'fr',fra:'fr',por:'pt',ger:'de',deu:'de',ita:'it',jpn:'ja',zho:'zh',chi:'zh',kor:'ko',rus:'ru',dut:'nl',nld:'nl'};
const names={en:'English',es:'Spanish',ar:'Arabic',fr:'French',pt:'Portuguese',de:'German',it:'Italian',ja:'Japanese',zh:'Chinese',ko:'Korean',ru:'Russian',nl:'Dutch',und:'Unknown language'};
export const textSubtitle=codec=>['','subrip','srt','ass','ssa','webvtt','mov_text','text'].includes(codec||'');
export const subtitleKey=track=>track.externalPath?'external:'+track.externalPath:'stream:'+track.streamIndex;
export function subtitleLabel(track){
 const language=languages[track.language]||track.language||'und';
 const base=names[language]||language;
 const title=(track.name||'').trim();
 let name=base;
 if(title&&title.toLowerCase()!==base.toLowerCase()&&title.toLowerCase()!==(track.language||'').toLowerCase()&&title!=='Subtitles')name+=' — '+title;
 if(track.sdh&&!/sdh/i.test(title))name+=' · SDH';
 if(track.forced&&!/forced/i.test(title))name+=' · Forced';
 if(track.externalPath)name+=' · External';
 return name+' · Track '+track.id;
}
export function subtitleSpecs(tracks,delay=0){
 return tracks.filter(t=>t.type==='sub').map(t=>{
  let externalPath='';
  if(t.external)try{externalPath=decodeURIComponent((t['external-filename']||'').replace(/^file:\/\//,''));}catch{externalPath=t['external-filename']||'';}
  return {id:t.id,streamIndex:t['ff-index']??-1,codec:t.codec||'',externalPath,name:t.title||'',language:t.lang||'und',delay,sdh:!!t['hearing-impaired'],forced:!!t.forced};
 });
}
export function subtitleChoices(specs){return specs.map(s=>({id:s.id,key:subtitleKey(s),name:subtitleLabel(s),language:languages[s.language]||s.language,sdh:s.sdh,forced:s.forced,external:!!s.externalPath,codec:s.codec,supported:textSubtitle(s.codec)}));}
