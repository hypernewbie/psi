// Regenerate public metadata only. Never point this tool at a Pi agent directory.
import {readFileSync,writeFileSync,mkdirSync} from 'node:fs';
import {join,resolve} from 'node:path';
import {createHash} from 'node:crypto';
const [packageArg,outputArg]=process.argv.slice(2);
if(!packageArg||!outputArg)throw new Error('usage: node scripts/snapshot-models.mjs PI_AI_PACKAGE OUTPUT_DIRECTORY');
const root=resolve(packageArg),out=resolve(outputArg);
const pkg=JSON.parse(readFileSync(join(root,'package.json'),'utf8'));
if(pkg.name!=='@earendil-works/pi-ai')throw new Error('input must be the public pi-ai package');
const catalog={},hashes={};
const manifest=JSON.parse(readFileSync(join(root,'dist/providers/data/.manifest.json'),'utf8'));
if(!manifest.generatedAt||Number.isNaN(Date.parse(manifest.generatedAt)))throw new Error('public catalog manifest lacks a valid generation date');
for(const provider of ['anthropic','openai-codex']){
 const data=readFileSync(join(root,'dist/providers/data',provider+'.json'));
 hashes[provider]=createHash('sha256').update(data).digest('hex');
 catalog[provider]=Object.values(JSON.parse(data)).flatMap(api=>Object.values(api)).filter(m=>m.type==='chat').map(m=>({id:m.id,api:m.api,contextWindow:m.contextWindow,maxTokens:m.maxTokens,reasoning:m.reasoning,input:m.input,...(m.thinkingLevelMap?{thinkingLevelMap:m.thinkingLevelMap}:{}),compat:{forceAdaptiveThinking:m.compat?.forceAdaptiveThinking===true,supportsMidConvoEffort:m.compat?.supportsMidConvoEffort===true}})).sort((a,b)=>a.id.localeCompare(b.id));
}
mkdirSync(out,{recursive:true});
writeFileSync(join(out,'catalog.json'),JSON.stringify(catalog,null,2)+'\n');
writeFileSync(join(out,'catalog-source.json'),JSON.stringify({package:pkg.name,version:pkg.version,generatedAt:manifest.generatedAt,sha256:hashes,protocolReference:'d5b3de22d0e24a3e9c38a9410d3329b099717283',generator:'snapshot-models-v1',scope:'all chat models in the two public provider catalogs; no credentials, endpoints, headers, or private configuration'},null,2)+'\n');
console.log('Public catalog snapshot generated: '+Object.entries(catalog).map(([p,v])=>p+'='+v.length).join(', '));
