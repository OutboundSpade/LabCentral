// Independent API client: no control-plane source imports, session cookies,
// user impersonation, SSH server, or unattended grant renewal.
import { request as httpsRequest } from 'node:https';
export class GatewayClient {
  constructor(origin, clientId, secret, { ca } = {}) {
    const parsed = new URL(origin);
    if (parsed.protocol !== 'https:' || parsed.origin !== origin) throw new Error('Exact HTTPS origin required');
    this.origin=origin; this.clientId=clientId; this.secret=secret; this.ca=ca;
  }
  async request(path, body, token, idempotencyKey) {
    if(!path.startsWith('/api/v1/')||path.includes('..')||path.includes('?'))throw new Error('Invalid API path');
    const encoded=body instanceof URLSearchParams?body.toString():body===undefined?undefined:JSON.stringify(body);
    const headers={};
    if(encoded!==undefined)headers['Content-Type']=body instanceof URLSearchParams?'application/x-www-form-urlencoded':'application/json';
    if(token){headers.Authorization=`Bearer ${token}`;headers['X-Gateway-Client']=this.clientId;headers['X-Gateway-Secret']=this.secret;}
    else headers.Authorization=`Basic ${Buffer.from(`${encodeURIComponent(this.clientId)}:${encodeURIComponent(this.secret)}`).toString('base64')}`;
    if(idempotencyKey)headers['Idempotency-Key']=idempotencyKey;
    return new Promise((resolve,reject)=>{const req=httpsRequest(this.origin+path,{method:encoded===undefined?'GET':'POST',headers,ca:this.ca,timeout:15000},res=>{let length=0;const chunks=[];res.on('data',chunk=>{length+=chunk.length;if(length>1048576){req.destroy(new Error('Response too large'));return;}chunks.push(chunk);});res.on('end',()=>{try{resolve({status:res.statusCode,body:JSON.parse(Buffer.concat(chunks).toString()||'{}')});}catch(e){reject(e);}});});req.on('error',reject);req.on('timeout',()=>req.destroy(new Error('Gateway request timeout')));req.end(encoded);});
  }
  begin(scope){return this.request('/api/v1/device/authorization',new URLSearchParams({client_id:this.clientId,scope:JSON.stringify(scope)}));}
  poll(code){return this.request('/api/v1/device/token',new URLSearchParams({client_id:this.clientId,device_code:code,grant_type:'urn:ietf:params:oauth:grant-type:device_code'}));}
  templates(token){return this.request('/api/v1/gateway/templates',undefined,token);}
  revisions(token,template){return this.request(`/api/v1/gateway/templates/${encodeURIComponent(template)}/revisions`,undefined,token);}
  deploy(token,request,key){return this.request('/api/v1/gateway/deployments',request,token,key);}
  operation(token,id){return this.request(`/api/v1/gateway/operations/${encodeURIComponent(id)}`,undefined,token);}
  access(token,request,key){return this.request('/api/v1/gateway/access-sessions',request,token,key);}
}
