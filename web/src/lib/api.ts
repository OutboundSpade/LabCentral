import {writable,get} from 'svelte/store';
import type {Overview} from './types';
export const session=writable<Overview|null>(null);
export const loading=writable(true);
export const loadError=writable('');
let pending:Promise<void>|null=null;
export async function refresh(){
 if(pending)return pending;
 pending=(async()=>{try{const res=await fetch('/api/me',{cache:'no-store'});if(res.status===401){session.set(null);loadError.set('');return}if(!res.ok)throw new Error('Your lab information could not be loaded. Please retry.');session.set(await res.json());loadError.set('')}catch(e){loadError.set(e instanceof Error?e.message:'Connection unavailable.')}finally{loading.set(false);pending=null}})();return pending;
}
export async function api<T>(path:string,body?:unknown):Promise<T>{
 const res=await fetch(path,{method:body===undefined?'GET':'POST',headers:body===undefined?{}:{'Content-Type':'application/json','X-CSRF-Token':get(session)?.csrf??''},body:body===undefined?undefined:JSON.stringify(body),cache:'no-store'});
 const result=await res.json();if(!res.ok)throw new Error(result.error??'Request failed. Please try again.');return result;
}
export function date(value:string){return new Date(value).toLocaleTimeString([],{hour:'numeric',minute:'2-digit'})}
