// Package after core/UI are built; imports only the published browser SDK.
import {readFile,writeFile,mkdir,chmod} from 'node:fs/promises';
import {resolve,join} from 'node:path';
import {createHash} from 'node:crypto';
const output=resolve(process.argv[2] ?? 'bin/external-browser');
await mkdir(output,{recursive:true,mode:0o700});await chmod(output,0o700);
const assets=[];
for(const [name,source] of [['echo.html',new URL('./echo.html',import.meta.url)],['echo.js',new URL('./echo.js',import.meta.url)],['index.html',new URL('./index.html',import.meta.url)],['panel.js',new URL('./panel.js',import.meta.url)],['page.html',new URL('./page.html',import.meta.url)],['page.js',new URL('./page.js',import.meta.url)],['sdk.js',new URL('../../pkg/plugin-sdk-browser/index.js',import.meta.url)]]){
 const bytes=await readFile(source);await writeFile(join(output,name),bytes,{mode:0o600});assets.push({path:name,sha256:createHash('sha256').update(bytes).digest('hex')});
}
const manifest={apiVersion:'labcentral.io/plugin-manifest/v2',id:'fixture.browser',version:'1.0.0',categories:['browser'],protocolMajor:1,minimumProtocolMinor:0,maximumProtocolMinor:0,operatingSystems:['linux','darwin','windows'],architectures:['amd64','arm64'],requestedPermissions:['context.read','topology.read','access.create','access.stream','templates.read','templates.write'],browser:{apiVersion:'labcentral.browser/v1',minimumHostVersion:2,assets,contributions:[{id:'page',slot:'page',title:'Independent feature fixture',entrypoint:'page.html',permissions:['context.read','templates.read','templates.write']},{id:'panel',slot:'plugin-settings',title:'Independent extension fixture',entrypoint:'index.html',permissions:['context.read']},{id:'lab-panel',slot:'lab-panel',title:'Fixture lab panel',entrypoint:'index.html',permissions:['context.read']},{id:'resource',slot:'resource-action',title:'Fixture resource action',entrypoint:'index.html',permissions:['context.read']},{id:'echo',slot:'access-view',title:'Fixture terminal',entrypoint:'echo.html',permissions:['context.read','topology.read','access.create','access.stream','templates.read','templates.write']}]}};
await writeFile(join(output,'plugin.json'),JSON.stringify(manifest,null,2)+'\n',{mode:0o600});
const installation={apiVersion:'labcentral.io/installation/v2',pluginRoot:output,extensions:[{manifest:'plugin.json',instanceId:'fixture.browser',grantedPermissions:['context.read','topology.read','access.create','access.stream','templates.read','templates.write']}]};
await writeFile(join(output,'installation.json'),JSON.stringify(installation,null,2)+'\n',{mode:0o600});
console.log(output);
