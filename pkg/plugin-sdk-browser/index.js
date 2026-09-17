// Framework-independent browser SDK. The host is the sole API/credential broker.
export function connect() {
  return new Promise((resolve) => {
    const listener = (event) => {
      if (event.source !== window.parent || event.data?.type !== 'labcentral.connect' || event.data.version !== 1 || typeof event.data.nonce !== 'string' || event.ports.length !== 1) return;
      window.removeEventListener('message', listener);
      const port = event.ports[0], pending = new Map(), subscribers = new Set();
      port.onmessage = ({data}) => {
        if (data?.version === 1 && ['stream.open','stream.data','stream.closed'].includes(data.type)) { for (const subscriber of subscribers) subscriber(data); return; }
        const request = pending.get(data?.id); if (!request || data.version !== 1) return;
        pending.delete(data.id); clearTimeout(request.timer);
        if (data.error) request.reject(new Error(data.error)); else request.resolve(data.result);
      };
      port.postMessage({version: 1, id: 'ready', method: 'ready', nonce: event.data.nonce});
      resolve({
        subscribe(listener) { subscribers.add(listener); return () => subscribers.delete(listener); },
        request(method, parameters = {}) {
          if (pending.size >= 4) return Promise.reject(new Error('Too many pending requests'));
          return new Promise((resolve, reject) => {
            const id = crypto.randomUUID();
            const timer = setTimeout(() => { pending.delete(id); reject(new Error('Host request timed out')); }, 15000);
            pending.set(id, {resolve, reject, timer}); port.postMessage({...parameters, version: 1, id, method});
          });
        },
        close() { subscribers.clear(); port.close(); for (const p of pending.values()) {clearTimeout(p.timer);p.reject(new Error('Bridge closed'));} pending.clear(); }
      });
    };
    window.addEventListener('message', listener);
  });
}
