declare module '@novnc/novnc' {
  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, channel: WebSocket, options?: { shared?: boolean });
    scaleViewport: boolean;
    resizeSession: boolean;
    viewOnly: boolean;
    sendCtrlAltDel(): void;
    sendKey(keysym: number, code?: string, down?: boolean): void;
    disconnect(): void;
    focus(): void;
  }
}
