// Build an isolated, dependency-minimal adapter around the unmodified pinned
// session reader. Requires Node 24. No provider factories or credentials load.
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { resolve, dirname, join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';

const [sourceArg, outputArg] = process.argv.slice(2);
if (!sourceArg || !outputArg) throw new Error('usage: node scripts/build-reference.mjs SOURCE_CHECKOUT OUTPUT_MODULE');
const source = resolve(sourceArg), output = resolve(outputArg);
const pinned = 'd5b3de22d0e24a3e9c38a9410d3329b099717283';
const revision = execFileSync('git', ['-C', source, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();
if (revision !== pinned) throw new Error('reference revision does not match the pin');
if (execFileSync('git', ['-C', source, 'status', '--porcelain', '--untracked-files=no'], {encoding:'utf8'}).trim()) throw new Error('reference source has tracked changes');
const reader = join(source, 'packages/coding-agent/src/core/session-manager.ts');
const wordNavigation = join(source, 'packages/tui/src/word-navigation.ts');
const ai = join(source, 'packages/ai/src/utils');
// cross-spawn is imported by the reference's platform wrapper. Linux uses the
// native node:child_process branch. Resolve the public installed dependency,
// never an agent directory, auth store, extension, or user configuration.
const dependencies = join(dirname(dirname(process.execPath)), 'lib/node_modules/@earendil-works/pi-coding-agent/node_modules');
const facade = `export {getCurrentSystemMessage} from ${JSON.stringify(pathToFileURL(join(ai,'transcript.ts')).href)}; export {uuidv7} from ${JSON.stringify(pathToFileURL(join(ai,'uuid.ts')).href)};`;
mkdirSync(dirname(output), {recursive:true});
writeFileSync(output, `import {registerHooks,stripTypeScriptTypes} from 'node:module';
import {readFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {createRequire} from 'node:module';
const require = createRequire(${JSON.stringify(pathToFileURL(join(dependencies,'reference-loader.cjs')).href)});
registerHooks({resolve(specifier,context,next){
 if(specifier==='@earendil-works/pi-ai')return {url:'data:text/javascript,'+encodeURIComponent(${JSON.stringify(facade)}),shortCircuit:true};
 if(specifier==='cross-spawn'||specifier==='get-east-asian-width')return {url:new URL('file://'+require.resolve(specifier)).href,shortCircuit:true};
 return next(specifier,context);
},load(url,context,next){
 if(url.startsWith('file:')&&url.endsWith('.ts'))return {format:'module',source:stripTypeScriptTypes(readFileSync(fileURLToPath(url),'utf8'),{mode:'transform'}),shortCircuit:true};
 return next(url,context);
}});
const reader=await import(${JSON.stringify(pathToFileURL(reader).href)});
export const {SessionManager,loadEntriesFromFile,buildSessionProjection,buildSessionContext}=reader;
export async function findWordBackward(...args) {
 const word=await import(${JSON.stringify(pathToFileURL(wordNavigation).href)});
 return word.findWordBackward(...args);
}
`);
writeFileSync(output+'.manifest.json',JSON.stringify({revision,readerSha256:createHash('sha256').update(readFileSync(reader)).digest('hex'),wordNavigationSha256:createHash('sha256').update(readFileSync(wordNavigation)).digest('hex'),adapter:'native-node-types-v2',sourceUnmodified:true},null,2)+'\n');
console.log('Pinned reference adapter built (unmodified reader and pinned transcript/UUID exports).');
