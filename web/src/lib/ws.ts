"use client";

import { useEffect, useRef, useState } from "react";
import { ensureSessionMigrated, getSessionEnvelope, subscribeSessionChanged } from "./api";
import type { SSEEvent, SSEHandler } from "./sse";

export type WSEvent = SSEEvent | "gate.status" | "ws.connected";

export interface EditorGateStatusPayload {
  version: number;
  instanceId: string;
  revision: number;
  generation: number;
  completedGeneration: number;
  running: boolean;
  lastRun: string;
}

const API_BASE = process.env.NEXT_PUBLIC_API_BASE
  ? process.env.NEXT_PUBLIC_API_BASE.replace(/\/api$/, "")
  : typeof window !== "undefined"
  ? window.location.origin
  : "";

function getWSUrl(token: string): string {
  let base = API_BASE;
  if (!base && typeof window !== "undefined") {
    base = window.location.origin;
  }
  const wsProto = base.startsWith("https") ? "wss" : "ws";
  const httpHost = base.replace(/^https?:\/\//, "");
  return `${wsProto}://${httpHost}/ws?token=${encodeURIComponent(token)}`;
}

function sessionVersion(): string {
  const envelope = getSessionEnvelope();
  return `${envelope?.epoch || ""}\u0000${envelope?.session?.token || ""}`;
}

export function useWebSocket(handler: SSEHandler, enabled: boolean) {
  const handlerRef = useRef(handler);
  handlerRef.current = handler;
  const wsRef = useRef<WebSocket | null>(null);
  const [version, setVersion] = useState(sessionVersion);

  useEffect(() => subscribeSessionChanged(() => {
    if (wsRef.current) handlerRef.current("sse.disconnected", { at: Date.now(), sessionChanged: true });
    wsRef.current?.close();
    setVersion(sessionVersion());
  }), []);

  useEffect(() => {
    if (!enabled) return;
    const initial = getSessionEnvelope();
    if (!initial?.session?.token) return;

    let isSubscribed = true;
    let reconnectTimer: NodeJS.Timeout | null = null;
    let attempt = 0;
    let hasOpened = false;

    const connect = async () => {
      if (!isSubscribed) return;
      try {
        await ensureSessionMigrated();
      } catch {
        // ignore
      }
      const envelope = getSessionEnvelope();
      const token = envelope?.session?.token;
      if (!token || !isSubscribed) return;

      const url = getWSUrl(token);
      const ws = new WebSocket(url);
      wsRef.current = ws;

      ws.onopen = () => {
        attempt = 0;
        if (hasOpened) {
          handlerRef.current("sse.reconnected", { at: Date.now() });
          handlerRef.current("sse.missed-events", { at: Date.now() });
        }
        hasOpened = true;
      };

      ws.onmessage = (event) => {
        let parsed: { event?: unknown; data?: unknown } | null = null;
        try {
          parsed = JSON.parse(event.data) as { event?: unknown; data?: unknown } | null;
        } catch {
          // ignore non-JSON frames
          return;
        }
        if (!parsed || typeof parsed.event !== "string") return;

        // The server's application-level heartbeat also keeps its read
        // deadline alive. Consume it at the transport boundary so it neither
        // reaches UI handlers nor causes an editor-gate status response.
        if (parsed.event === "ping") {
          if (ws.readyState === WebSocket.OPEN) {
            try {
              ws.send(JSON.stringify({ type: "pong" }));
            } catch {
              ws.close();
            }
          }
          return;
        }

        handlerRef.current(parsed.event as SSEEvent, parsed.data);
      };

      ws.onclose = () => {
        if (wsRef.current === ws) wsRef.current = null;
        if (!isSubscribed) return;
        handlerRef.current("sse.disconnected", { at: Date.now() });
        const delay = Math.min(1000 * (2 ** Math.min(attempt++, 5)), 10000);
        reconnectTimer = setTimeout(connect, delay);
      };

      ws.onerror = () => {
        ws.close();
      };
    };

    void connect();

    return () => {
      isSubscribed = false;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      if (wsRef.current) {
        wsRef.current.close();
        wsRef.current = null;
      }
    };
  }, [enabled, version]);
}
