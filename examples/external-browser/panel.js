import {connect} from './sdk.js';
const bridge = await connect();
const context = await bridge.request('context.read');
document.querySelector('#status').textContent = `Connected: ${context.pluginInstance}`;
let cookieDenied=false, storageDenied=false, parentDenied=false;
try { document.cookie; } catch { cookieDenied=true; }
try { localStorage.getItem('canary'); } catch { storageDenied=true; }
try { window.parent.document.cookie; } catch { parentDenied=true; }
document.querySelector('#isolation').textContent = JSON.stringify({cookieDenied,storageDenied,parentDenied});
document.querySelector('#probe').onclick = async () => {
 try { await bridge.request('http.fetch', {url:'/api/v1/session'});document.querySelector('#denial').textContent='UNSAFE'; }
 catch { document.querySelector('#denial').textContent='Denied'; }
};
