const DEFAULT_MIN_RETRY_MS = 500;
const DEFAULT_MAX_RETRY_MS = 10_000;
const DEFAULT_CONNECT_TIMEOUT_MS = 15_000;
const DEFAULT_HEARTBEAT_TIMEOUT_MS = 60_000;

export function boundedReconnectDelay(attempt, serverRetry, minRetry = DEFAULT_MIN_RETRY_MS, maxRetry = DEFAULT_MAX_RETRY_MS) {
  const base = Number.isSafeInteger(serverRetry) ? serverRetry : minRetry;
  return Math.min(Math.max(base * (2 ** Math.min(Math.max(attempt, 0), 8)), minRetry), maxRetry);
}

export class SSEParser {
  constructor(onEvent, onRetry = () => {}, onActivity = () => {}) {
    this.onEvent = onEvent;
    this.onRetry = onRetry;
    this.onActivity = onActivity;
    this.buffer = "";
    this.event = "";
    this.data = [];
    this.hasData = false;
    this.firstLine = true;
  }

  push(chunk) {
    this.buffer += chunk;
    let newline;
    while ((newline = this.buffer.indexOf("\n")) !== -1) {
      let line = this.buffer.slice(0, newline);
      this.buffer = this.buffer.slice(newline + 1);
      if (line.endsWith("\r")) line = line.slice(0, -1);
      this.processLine(line);
    }
  }

  finish() {
    if (this.buffer) {
      let line = this.buffer;
      if (line.endsWith("\r")) line = line.slice(0, -1);
      this.processLine(line);
    }
    this.buffer = "";
    this.dispatch();
  }

  processLine(line) {
    if (this.firstLine) {
      this.firstLine = false;
      if (line.startsWith("\uFEFF")) line = line.slice(1);
    }
    if (line === "") {
      this.dispatch();
      return;
    }
    if (line.startsWith(":")) {
      this.onActivity();
      return;
    }

    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") {
      this.onActivity();
      this.event = value;
    } else if (field === "data") {
      this.onActivity();
      this.data.push(value);
      this.hasData = true;
    } else if (field === "retry" && /^[0-9]+$/.test(value)) {
      const retry = Number(value);
      if (Number.isSafeInteger(retry)) {
        this.onActivity();
        this.onRetry(retry);
      }
    }
  }

  dispatch() {
    if (this.hasData) this.onEvent(this.event || "message", this.data.join("\n"));
    this.event = "";
    this.data = [];
    this.hasData = false;
  }
}

export function abortableDelay(delay, signal) {
  if (signal.aborted) return Promise.reject(signal.reason);
  return new Promise((resolve, reject) => {
    const timer = setTimeout(done, delay);
    function done() {
      signal.removeEventListener("abort", aborted);
      resolve();
    }
    function aborted() {
      clearTimeout(timer);
      signal.removeEventListener("abort", aborted);
      reject(signal.reason);
    }
    signal.addEventListener("abort", aborted, { once: true });
  });
}

async function cancelBody(response) {
  try {
    await response.body?.cancel();
  } catch {
    // The response is already closed.
  }
}

export async function runFetchSSE({
  url,
  signal,
  headers = {},
  getSession,
  onEvent,
  onOpen = () => {},
  onDisconnect = () => {},
  onReconnect = () => {},
  onMissedEvents = () => {},
  onUnauthorized = async (_session) => true,
  fetchImpl = globalThis.fetch,
  wait = abortableDelay,
  minRetry = DEFAULT_MIN_RETRY_MS,
  maxRetry = DEFAULT_MAX_RETRY_MS,
  connectTimeout = DEFAULT_CONNECT_TIMEOUT_MS,
  heartbeatTimeout = DEFAULT_HEARTBEAT_TIMEOUT_MS,
}) {
  let attempt = 0;
  let serverRetry;
  let gapPending = false;
  while (!signal.aborted) {
    const session = await getSession(signal);
    if (!session?.token || signal.aborted) return;

    const attemptController = new AbortController();
    const abortAttempt = () => attemptController.abort(signal.reason);
    signal.addEventListener("abort", abortAttempt, { once: true });
    const connectionTimer = setTimeout(() => {
      attemptController.abort(new Error("SSE connection timed out"));
    }, connectTimeout);
    let response;
    try {
      response = await fetchImpl(url, {
        headers: { Accept: "text/event-stream", Authorization: `Bearer ${session.token}`, ...headers },
        cache: "no-store",
        signal: attemptController.signal,
      });
    } catch (reason) {
      clearTimeout(connectionTimer);
      signal.removeEventListener("abort", abortAttempt);
      if (signal.aborted) return;
      gapPending = true;
      onDisconnect(reason);
      await wait(boundedReconnectDelay(attempt++, serverRetry, minRetry, maxRetry), signal);
      continue;
    }
    if (response.status === 401) {
      clearTimeout(connectionTimer);
      signal.removeEventListener("abort", abortAttempt);
      await cancelBody(response);
      if (await onUnauthorized(session) !== false) return;
      attempt = 0;
      continue;
    }
    const contentType = response.headers.get("Content-Type") || "";
    if (!response.ok || !response.body || !contentType.toLowerCase().startsWith("text/event-stream")) {
      clearTimeout(connectionTimer);
      signal.removeEventListener("abort", abortAttempt);
      await cancelBody(response);
      if (signal.aborted) return;
      gapPending = true;
      onDisconnect(new Error(`SSE request failed with status ${response.status}`));
      await wait(boundedReconnectDelay(attempt++, serverRetry, minRetry, maxRetry), signal);
      continue;
    }
    clearTimeout(connectionTimer);

    const reconnected = gapPending;
    let receivedEvent = false;
    let openedConnection = false;
    let heartbeatTimer;
    const armWatchdog = (delay, message) => {
      clearTimeout(heartbeatTimer);
      heartbeatTimer = setTimeout(() => {
        attemptController.abort(new Error(message));
      }, delay);
    };
    const markActivity = () => {
      if (!openedConnection) {
        openedConnection = true;
        onOpen({ reconnected });
        if (reconnected) {
          onReconnect();
          onMissedEvents();
          gapPending = false;
        }
      }
      armWatchdog(heartbeatTimeout, "SSE heartbeat timed out");
    };
    const parser = new SSEParser((event, data) => {
      receivedEvent = true;
      onEvent(event, data);
    }, retry => {
      serverRetry = Math.min(Math.max(retry, minRetry), maxRetry);
    }, markActivity);
    const decoder = new TextDecoder();
    const reader = response.body.getReader();
    let disconnectReason;
    // Headers alone do not establish a live stream. Require a complete valid
    // SSE line before switching to the longer heartbeat watchdog.
    armWatchdog(connectTimeout, "SSE connection timed out");
    try {
      while (!signal.aborted) {
        const { done, value } = await reader.read();
        if (done) break;
        parser.push(decoder.decode(value, { stream: true }));
      }
      if (!signal.aborted) {
        parser.push(decoder.decode());
        parser.finish();
      }
    } catch (reason) {
      if (signal.aborted) return;
      disconnectReason = reason;
    } finally {
      clearTimeout(heartbeatTimer);
      signal.removeEventListener("abort", abortAttempt);
      if (attemptController.signal.aborted) {
        try { await reader.cancel(); } catch { /* stream is already closed */ }
      }
      reader.releaseLock();
    }
    if (signal.aborted) return;
    gapPending = true;
    onDisconnect(disconnectReason || new Error("SSE stream closed"));
    attempt = receivedEvent ? 0 : attempt + 1;
    await wait(boundedReconnectDelay(attempt, serverRetry, minRetry, maxRetry), signal);
  }
}
