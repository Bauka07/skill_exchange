import { useChatStore } from '@/store/chatStore';
import { ServerMessage } from '@/types/chat';

class ChatSocket {
  private ws: WebSocket | null = null;
  private reconnectAttempts = 0;
  private maxReconnectAttempts = 20;
  private heartbeatInterval: ReturnType<typeof setInterval> | null = null;
  private reconnectTimeout: ReturnType<typeof setTimeout> | null = null;
  private listeners: Map<string, Set<(payload: unknown) => void>> = new Map();
  private lastRoomId: string | null = null;
  private lastToken: string | null = null;
  private isReconnecting = false;
  private hasReportedError = false;
  private hasReportedSendIssue = false;
  private intentionalDisconnect = false;

  connect(roomId: string, token: string) {
    if (
      this.ws &&
      this.ws.readyState === WebSocket.OPEN &&
      this.lastRoomId === roomId &&
      this.lastToken === token
    ) {
      useChatStore.getState().setStatus('chatting');
      return;
    }

    this.lastRoomId = roomId;
    this.lastToken = token;
    this.intentionalDisconnect = false;

    // Cancel any pending reconnect
    if (this.reconnectTimeout) {
      clearTimeout(this.reconnectTimeout);
      this.reconnectTimeout = null;
    }

    if (this.ws) {
      // Remove handlers before closing to prevent reconnect loop
      this.ws.onclose = null;
      this.ws.onerror = null;
      this.ws.onmessage = null;
      this.ws.onopen = null;
      this.ws.close();
      this.ws = null;
    }

    this.isReconnecting = false;
    this.hasReportedError = false;
    this.stopHeartbeat();

    const wsUrl = this.getWebSocketBaseUrl();
    if (!wsUrl) {
      console.error('NEXT_PUBLIC_WS_URL is not defined in environment!');
      return;
    }
    const finalUrl = `${wsUrl}?room_id=${roomId}&token=${token}`;
    console.log('Connecting to:', finalUrl);

    this.ws = new WebSocket(finalUrl);

    this.ws.onopen = () => {
      console.log('Connected to chat');
      this.isReconnecting = false;
      this.reconnectAttempts = 0;
      this.hasReportedSendIssue = false;
      useChatStore.getState().setStatus('chatting');
      this.startHeartbeat();
    };

    this.ws.onmessage = (e: MessageEvent) => {
      try {
        const msg: ServerMessage = JSON.parse(e.data);
        if (msg.type === 'pong') return;
        this.handleMessage(msg);
      } catch (err) {
        console.error('Failed to parse WS message', err);
      }
    };

    this.ws.onclose = (e: CloseEvent) => {
      console.log('Disconnected from chat', e.code, e.reason);
      this.stopHeartbeat();

      if (this.intentionalDisconnect) return;

      // Prevent multiple simultaneous reconnect loops
      if (this.isReconnecting) return;

      const status = useChatStore.getState().status;

      if (status !== 'idle' && status !== 'ended' && this.reconnectAttempts < this.maxReconnectAttempts) {
        this.isReconnecting = true;
        this.reconnectAttempts++;
        const delay = Math.min(1000 * Math.pow(1.5, this.reconnectAttempts), 10000);
        console.log(`Attempting reconnect ${this.reconnectAttempts} in ${delay}ms...`);
        this.reconnectTimeout = setTimeout(() => {
          this.isReconnecting = false;
          if (this.lastRoomId && this.lastToken) {
            this.connect(this.lastRoomId, this.lastToken);
          }
        }, delay);
      } else if (status !== 'idle' && status !== 'ended') {
        useChatStore.getState().setStatus('ended');
        useChatStore.getState().setEndReason('disconnect');
      }
    };

    this.ws.onerror = () => {
      if (this.hasReportedError) return;
      this.hasReportedError = true;
      console.warn('Chat socket connection issue; reconnecting if possible.');
    };
  }

  private getWebSocketBaseUrl(): string {
    const configured = (process.env as Record<string, string | undefined>).NEXT_PUBLIC_WS_URL;

    if (
      configured &&
      typeof window !== 'undefined' &&
      window.location.hostname &&
      (window.location.hostname === 'localhost' || window.location.hostname === '127.0.0.1')
    ) {
      try {
        const configuredUrl = new URL(configured);
        const isLocalSocket =
          configuredUrl.hostname === 'localhost' || configuredUrl.hostname === '127.0.0.1';

        if (isLocalSocket) {
          return `${configuredUrl.protocol}//${window.location.hostname}:${configuredUrl.port}${configuredUrl.pathname}`;
        }
      } catch {
        return configured;
      }
    }

    return configured ?? '';
  }

  private handleMessage(msg: ServerMessage) {
    const store = useChatStore.getState();

    switch (msg.type) {
      case 'partner_connected':
        store.setStatus('chatting');
        break;
      case 'message':
        if (msg.content) {
          if (msg.sender === 'me') return;
          store.addMessage({
            id: Math.random().toString(36).substring(7),
            sender: msg.sender || 'partner',
            content: msg.content,
            timestamp: msg.timestamp || msg.ts || Date.now(),
          });
        }
        break;
      case 'error':
        console.warn('Chat server rejected a socket message', msg);
        break;
      case 'partner_disconnected':
        store.setStatus('ended');
        store.setEndReason('disconnect');
        break;
      case 'partner_typing':
        store.setPartnerTyping(!!msg.is_typing);
        break;
    }

    // Notify listeners
    const typeListeners = this.listeners.get(msg.type);
    if (typeListeners) {
      typeListeners.forEach(cb => cb(msg.payload || msg));
    }
  }

  send(content: string): boolean {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: 'message', content }));
      useChatStore.getState().addMessage({
        id: Math.random().toString(36).substring(7),
        sender: 'me',
        content,
        timestamp: Date.now(),
      });
      return true;
    }

    if (!this.hasReportedSendIssue) {
      this.hasReportedSendIssue = true;
      console.warn('Chat socket is not connected; message was not sent.');
    }
    return false;
  }

  sendTyping(isTyping: boolean) {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: 'typing', is_typing: isTyping }));
    }
  }

  next() {
    this.intentionalDisconnect = true;
    const store = useChatStore.getState();
    store.setStatus('ended');
    store.setMode(null);
    store.setRoomId(null);
    store.setEndReason('next');

    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: 'next' }));
      setTimeout(() => {
        if (this.ws?.readyState === WebSocket.OPEN) {
          this.disconnect();
        }
      }, 150);
    } else {
      this.disconnect();
    }
  }

  onMessage(type: string, callback: (payload: unknown) => void) {
    if (!this.listeners.has(type)) {
      this.listeners.set(type, new Set());
    }
    this.listeners.get(type)!.add(callback);
  }

  offMessage(type: string, callback: (payload: unknown) => void) {
    const typeListeners = this.listeners.get(type);
    if (typeListeners) {
      typeListeners.delete(callback);
    }
  }

  sendRTC(type: string, payload: unknown) {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type, payload }));
    }
  }

  disconnect() {
    if (this.reconnectTimeout) {
      clearTimeout(this.reconnectTimeout);
      this.reconnectTimeout = null;
    }
    this.stopHeartbeat();
    this.isReconnecting = false;
    this.reconnectAttempts = 0;
    this.lastRoomId = null;
    this.lastToken = null;
    if (this.ws) {
      this.ws.onclose = null;
      this.ws.onerror = null;
      this.ws.onmessage = null;
      this.ws.onopen = null;
      this.ws.close();
      this.ws = null;
    }
  }

  private startHeartbeat() {
    this.stopHeartbeat();
    this.heartbeatInterval = setInterval(() => {
      if (this.ws?.readyState === WebSocket.OPEN) {
        this.ws.send(JSON.stringify({ type: 'ping' }));
      }
    }, 30000);
  }

  private stopHeartbeat() {
    if (this.heartbeatInterval) {
      clearInterval(this.heartbeatInterval);
      this.heartbeatInterval = null;
    }
  }
}

export const chatSocket = new ChatSocket();
