// Package from independent sources, then serve immutable fixture assets to the built UI.
import {readFile,mkdtemp} from 'node:fs/promises';
import {rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import {execFileSync} from 'node:child_process';
const directory=await mkdtemp(join(tmpdir(),'labcentral-feature-fixture-'));
process.on('exit',()=>rmSync(directory,{recursive:true,force:true}));
execFileSync(process.execPath,[new URL('../../features/package.mjs',import.meta.url).pathname,directory]);
export async function featureExtensions(origin,names=['template-library','teams','classroom']){const items=[];for(const name of names){const m=JSON.parse(await readFile(join(directory,name,'plugin.json'),'utf8'));for(const c of m.browser.contributions)items.push({id:c.id,pluginInstance:`feature.${name}`,slot:c.slot,title:c.title,routeAlias:c.routeAlias,icon:c.icon,requiredActions:c.requiredActions,permissions:c.permissions,url:`${origin}/__features/${name}/${c.entrypoint}`});}return {items};}
export async function serveFeature(req,res){const path=new URL(req.url,'http://local').pathname;if(!path.startsWith('/__features/'))return false;const name=path.slice('/__features/'.length),file=resolve(directory,name);if(!file.startsWith(directory+'/')){res.writeHead(404).end();return true;}try{const bytes=await readFile(file);res.writeHead(200,{'Access-Control-Allow-Origin':'*','Content-Type':name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':'text/html'});res.end(bytes);}catch{res.writeHead(404).end();}return true;}
export {directory as featureDirectory};
