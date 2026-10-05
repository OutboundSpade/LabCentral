import {writable,get} from 'svelte/store';
import type {Overview,AdminView} from './types';
export const session=writable<Overview|null>(null);
export const adminView=writable<AdminView|null>(null);
let adminScope='';
session.subscribe(value=>{const scope=value?.user.admin?`${value.user.id}:${value.csrf}`:'';if(scope!==adminScope){adminScope=scope;adminView.set(null)}});
let adminPending:{scope:string;promise:Promise<void>}|null=null;
export async function refreshAdmin(){
 await refresh();
 const scope=adminScope;
 if(!scope)return;
 if(adminPending?.scope===scope)return adminPending.promise;
 const promise=(async()=>{try{const data=await api<AdminView>('/api/admin');if(adminScope===scope)adminView.set(data)}catch(e){if(adminScope===scope)adminView.set(null);throw e}finally{if(adminPending?.scope===scope)adminPending=null}})();
 adminPending={scope,promise};return promise;
}
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
