// Protocol fixture for UI readiness and viewport behavior; no DCV simulation claim.
export default {
 LogLevel:{ERROR:0},setLogLevel(){},
 authenticate(url,callbacks){callbacks.success(null,[{sessionId:'',authToken:'fixture'}])},
 async connect(options){
  window.fixtureClipboardAuto=options.clipboardAutoSync;window.fixtureDisconnect=()=>options.callbacks.disconnect();
  const display=document.getElementById(options.divId);
  display.style.width='800px';display.style.height='600px';
  window.fixtureFirstFrame=()=>options.callbacks.firstFrame();
  const connection={captureClipboardEvents(enabled){window.fixtureClipboardCapture=enabled},syncClipboards(){window.fixtureClipboardCopy=true;return true},async requestResolution(width,height){window.fixtureResolution=[width,height];if(width<640)throw new Error('server minimum resolution');display.style.width=width+'px';display.style.height=height+'px'}};
  window.fixtureClipboardEstablished=()=>options.callbacks.clipboardEvent(connection,{name:'established'});
  return connection;
 }
};
