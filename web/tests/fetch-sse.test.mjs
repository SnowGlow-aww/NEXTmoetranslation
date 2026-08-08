import assert from "node:assert/strict";
import test from "node:test";
import { boundedReconnectDelay, runFetchSSE, SSEParser } from "../src/lib/fetch-sse.mjs";

test("SSE parser handles chunks, CRLF/LF, comments, names, multiline data, and final buffers", () => {
  const events = [];
  const retries = [];
  let activities = 0;
  const parser = new SSEParser((event, data) => events.push({ event, data }), retry => retries.push(retry), () => { activities++; });
  for (const chunk of [
    "\uFEFF: comment\r\nevent: entry.up",
    "dated\r\ndata: first\r\ndata:second\nretry: 1250\r\n\r",
    "\nevent: content.restored\ndata: final",
  ]) parser.push(chunk);
  parser.finish();

  assert.deepEqual(events, [
    { event: "entry.updated", data: "first\nsecond" },
    { event: "content.restored", data: "final" },
  ]);
  assert.deepEqual(retries, [1250]);
  assert.equal(activities, 7);
});

test("fetch SSE reconnects with bounded backoff and the current bearer header", async () => {
  const controller = new AbortController();
  const encoder = new TextEncoder();
  const tokens = ["token-a", "token-b"];
  const calls = [];
  const waits = [];
  const events = [];
  let session = 0;
  const fetchImpl = async (url, init) => {
    calls.push({ url, headers: new Headers(init.headers) });
    const token = tokens[calls.length - 1];
    return new Response(new ReadableStream({
      start(streamController) {
        streamController.enqueue(encoder.encode(`event: ping\r\ndata: ${token}`));
        streamController.close();
      },
    }), { headers: { "Content-Type": "text/event-stream; charset=utf-8" } });
  };

  await runFetchSSE({
    url: "/sse",
    signal: controller.signal,
    getSession: async () => ({ token: tokens[session++] }),
    fetchImpl,
    wait: async delay => { waits.push(delay); },
    onEvent: (event, data) => {
      events.push({ event, data });
      if (events.length === 2) controller.abort();
    },
  });

  assert.deepEqual(calls.map(call => call.url), ["/sse", "/sse"]);
  assert.deepEqual(calls.map(call => call.headers.get("Authorization")), ["Bearer token-a", "Bearer token-b"]);
  assert.ok(calls.every(call => !call.url.includes("token")));
  assert.deepEqual(events, [
    { event: "ping", data: "token-a" },
    { event: "ping", data: "token-b" },
  ]);
  assert.deepEqual(waits, [500]);
  assert.equal(boundedReconnectDelay(20, undefined), 10_000);
  assert.equal(boundedReconnectDelay(20, 3_000), 10_000);
});

test("fetch SSE stops on 401 without placing the credential in the URL or error", async () => {
  const calls = [];
  const unauthorized = [];
  await runFetchSSE({
    url: "/sse",
    signal: new AbortController().signal,
    getSession: async () => ({ token: "secret-session-token", epoch: "epoch-a" }),
    fetchImpl: async (url, init) => {
      calls.push({ url, authorization: new Headers(init.headers).get("Authorization") });
      return new Response('{"error":"unauthorized"}', { status: 401 });
    },
    onEvent: () => assert.fail("401 must not dispatch an event"),
    onUnauthorized: async session => { unauthorized.push(session.epoch); return true; },
  });

  assert.deepEqual(calls, [{ url: "/sse", authorization: "Bearer secret-session-token" }]);
  assert.deepEqual(unauthorized, ["epoch-a"]);
  assert.equal(JSON.stringify(calls.map(call => call.url)).includes("secret-session-token"), false);
});

test("fetch SSE aborts an in-flight connection without scheduling reconnect", async () => {
  const controller = new AbortController();
  const waits = [];
  const pending = runFetchSSE({
    url: "/sse",
    signal: controller.signal,
    getSession: async () => ({ token: "token" }),
    fetchImpl: (_url, init) => new Promise((_resolve, reject) => {
      init.signal.addEventListener("abort", () => reject(init.signal.reason), { once: true });
    }),
    wait: async delay => { waits.push(delay); },
    onEvent: () => {},
  });
  controller.abort(new DOMException("unmounted", "AbortError"));
  await pending;
  assert.deepEqual(waits, []);
});

test("fetch SSE reports disconnect, reconnect, and mandatory reconciliation after a gap", async () => {
  const controller = new AbortController();
  const encoder = new TextEncoder();
  const lifecycle = [];
  let connection = 0;
  let authoritativeText = "before-gap";
  let renderedText = authoritativeText;

  await runFetchSSE({
    url: "/sse",
    signal: controller.signal,
    getSession: async () => ({ token: "token" }),
    fetchImpl: async () => {
      connection++;
      return new Response(new ReadableStream({
        start(streamController) {
          streamController.enqueue(encoder.encode("event: ping\ndata: 1\n\n"));
          streamController.close();
        },
      }), { headers: { "Content-Type": "text/event-stream" } });
    },
    wait: async () => {
      authoritativeText = "remote edit during gap";
    },
    onEvent: () => {
      if (connection === 2) controller.abort();
    },
    onDisconnect: () => lifecycle.push("disconnected"),
    onReconnect: () => lifecycle.push("reconnected"),
    onMissedEvents: () => {
      lifecycle.push("missed-events");
      renderedText = authoritativeText;
    },
  });

  assert.deepEqual(lifecycle, ["disconnected", "reconnected", "missed-events"]);
  assert.equal(renderedText, "remote edit during gap");
});

test("fetch SSE reconnects when connection establishment stalls", async () => {
  const controller = new AbortController();
  const encoder = new TextEncoder();
  let calls = 0;
  const disconnects = [];
  await runFetchSSE({
    url: "/sse",
    signal: controller.signal,
    getSession: async () => ({ token: "token" }),
    connectTimeout: 5,
    fetchImpl: async (_url, init) => {
      calls++;
      if (calls === 1) {
        return new Promise((_resolve, reject) => {
          init.signal.addEventListener("abort", () => reject(init.signal.reason), { once: true });
        });
      }
      return new Response(new ReadableStream({
        start(streamController) {
          streamController.enqueue(encoder.encode("event: ping\ndata: 1\n\n"));
          streamController.close();
        },
      }), { headers: { "Content-Type": "text/event-stream" } });
    },
    wait: async () => {},
    onEvent: () => controller.abort(),
    onDisconnect: reason => disconnects.push(reason.message),
  });
  assert.equal(calls, 2);
  assert.deepEqual(disconnects, ["SSE connection timed out"]);
});

test("fetch SSE reconnects when an established stream stops heartbeating", async () => {
  const controller = new AbortController();
  const encoder = new TextEncoder();
  let calls = 0;
  const lifecycle = [];
  await runFetchSSE({
    url: "/sse",
    signal: controller.signal,
    getSession: async () => ({ token: "token" }),
    heartbeatTimeout: 5,
    connectTimeout: 5,
    fetchImpl: async (_url, init) => {
      calls++;
      if (calls === 1) {
        return new Response(new ReadableStream({
          start(streamController) {
            streamController.enqueue(encoder.encode(": connected\n\n"));
            init.signal.addEventListener("abort", () => streamController.error(init.signal.reason), { once: true });
          },
        }), { headers: { "Content-Type": "text/event-stream" } });
      }
      return new Response(new ReadableStream({
        start(streamController) {
          streamController.enqueue(encoder.encode("event: ping\ndata: 2\n\n"));
          streamController.close();
        },
      }), { headers: { "Content-Type": "text/event-stream" } });
    },
    wait: async () => {},
    onEvent: () => controller.abort(),
    onDisconnect: reason => lifecycle.push(reason.message),
    onReconnect: () => lifecycle.push("reconnected"),
    onMissedEvents: () => lifecycle.push("missed-events"),
  });
  assert.equal(calls, 2);
  assert.deepEqual(lifecycle, ["SSE heartbeat timed out", "reconnected", "missed-events"]);
});

test("fragment bytes without a complete SSE line cannot keep a connection alive", async () => {
  const controller = new AbortController();
  let calls = 0;
  const disconnects = [];
  await runFetchSSE({
    url: "/sse",
    signal: controller.signal,
    getSession: async () => ({ token: "token" }),
    connectTimeout: 8,
    fetchImpl: async (_url, init) => {
      calls++;
      if (calls > 1) {
        controller.abort();
        return new Response(null, { headers: { "Content-Type": "text/event-stream" } });
      }
      return new Response(new ReadableStream({
        start(streamController) {
          const interval = setInterval(() => streamController.enqueue(new Uint8Array([58])), 2);
          init.signal.addEventListener("abort", () => {
            clearInterval(interval);
            streamController.error(init.signal.reason);
          }, { once: true });
        },
      }), { headers: { "Content-Type": "text/event-stream" } });
    },
    wait: async () => {},
    onEvent: () => assert.fail("fragments must not dispatch"),
    onOpen: () => assert.fail("fragments must not open the logical stream"),
    onDisconnect: reason => disconnects.push(reason.message),
  });
  assert.equal(calls, 2);
  assert.deepEqual(disconnects, ["SSE connection timed out"]);
});
