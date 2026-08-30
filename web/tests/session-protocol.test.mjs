import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

const source = await readFile(new URL("../src/lib/session.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText;
const sessionURL = `data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`;
const session = await import(sessionURL);
const apiSource = await readFile(new URL("../src/lib/api.ts", import.meta.url), "utf8");
const catalogPaginationSource = await readFile(new URL("../src/lib/catalog-pagination.mjs", import.meta.url), "utf8");
const catalogPaginationURL = `data:text/javascript;base64,${Buffer.from(catalogPaginationSource).toString("base64")}`;
const lyricsVersioningSource = await readFile(new URL("../src/lib/lyrics-versioning.mjs", import.meta.url), "utf8");
const lyricsVersioningURL = `data:text/javascript;base64,${Buffer.from(lyricsVersioningSource).toString("base64")}`;
const lyricsEditionsSource = await readFile(new URL("../src/lib/lyrics-editions.mjs", import.meta.url), "utf8");
const lyricsEditionsURL = `data:text/javascript;base64,${Buffer.from(lyricsEditionsSource).toString("base64")}`;
const lyricsSaveSource = (await readFile(new URL("../src/lib/lyrics-save.mjs", import.meta.url), "utf8"))
  .replaceAll('"./lyrics-versioning.mjs"', JSON.stringify(lyricsVersioningURL))
  .replaceAll('"./lyrics-editions.mjs"', JSON.stringify(lyricsEditionsURL));
const lyricsSaveURL = `data:text/javascript;base64,${Buffer.from(lyricsSaveSource).toString("base64")}`;
const apiCompiled = ts.transpileModule(apiSource, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText
  .replaceAll('"./session"', JSON.stringify(sessionURL))
  .replaceAll('"./catalog-pagination.mjs"', JSON.stringify(catalogPaginationURL))
  .replaceAll('"./lyrics-save.mjs"', JSON.stringify(lyricsSaveURL));
const api = await import(`data:text/javascript;base64,${Buffer.from(apiCompiled).toString("base64")}`);

class MemoryStorage {
  #values = new Map();

  get length() { return this.#values.size; }
  clear() { this.#values.clear(); }
  getItem(key) { return this.#values.get(String(key)) ?? null; }
  key(index) { return [...this.#values.keys()][index] ?? null; }
  removeItem(key) { this.#values.delete(String(key)); }
  setItem(key, value) { this.#values.set(String(key), String(value)); }
}

class TestStorageEvent extends Event {
  constructor(type, init) {
    super(type);
    Object.assign(this, init);
  }
}

function installBrowser() {
  const localStorage = new MemoryStorage();
  const window = new EventTarget();
  window.localStorage = localStorage;
  window.location = { reload() {} };
  const tails = new Map();
  const locks = {
    request(name, _options, callback) {
      const previous = tails.get(name) || Promise.resolve();
      const current = previous.then(() => callback());
      tails.set(name, current.catch(() => undefined));
      return current;
    },
  };
  Object.defineProperty(globalThis, "window", { configurable: true, value: window });
  Object.defineProperty(globalThis, "localStorage", { configurable: true, value: localStorage });
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: { locks } });
  Object.defineProperty(globalThis, "StorageEvent", { configurable: true, value: TestStorageEvent });
  return { localStorage, window };
}

async function installValidSession() {
  installBrowser();
  await session.ensureSessionMigrated();
  const initial = session.getStoredSessionEnvelope();
  await session.commitIdentitySession(valid("ignored", "token-a").session, initial);
  return session.getStoredSessionEnvelope();
}

function valid(epoch = "epoch-a", token = "token-a", role = "editor") {
  return {
    version: 1,
    epoch,
    session: { token, username: "translator", role, expiresAt: Math.floor(Date.now() / 1000) + 3600 },
  };
}

test("session envelope parsing rejects unknown roles and malformed expiries", () => {
  installBrowser();
  assert.deepEqual(session.parseSessionEnvelope(JSON.stringify(valid())), valid());
  assert.equal(session.parseSessionEnvelope(JSON.stringify(valid("epoch", "token", "viewer"))), null);
  assert.equal(session.parseSessionEnvelope(JSON.stringify({ ...valid(), session: { ...valid().session, expiresAt: 1.5 } })), null);
  assert.equal(session.parseSessionEnvelope(JSON.stringify({ version: 2, epoch: "epoch", session: null })), null);
});

test("legacy split keys migrate once without replacing a valid envelope", async () => {
  const { localStorage } = installBrowser();
  const newer = valid("newer", "new-token", "admin");
  localStorage.setItem(session.SESSION_KEY, JSON.stringify(newer));
  localStorage.setItem("moesekai-token", "legacy-token");
  localStorage.setItem("moesekai-user", "legacy-user");
  localStorage.setItem("moesekai-role", "editor");
  localStorage.setItem("moesekai-expires-at", String(Math.floor(Date.now() / 1000) + 3600));

  await session.ensureSessionMigrated();

  assert.deepEqual(session.getStoredSessionEnvelope(), newer);
  assert.equal(localStorage.getItem("moesekai-token"), "legacy-token");
});

test("a valid legacy split session becomes one atomic envelope and is cleaned up", async () => {
  const { localStorage } = installBrowser();
  localStorage.setItem("moesekai-token", "legacy-token");
  localStorage.setItem("moesekai-user", "legacy-user");
  localStorage.setItem("moesekai-role", "editor");
  localStorage.setItem("moesekai-expires-at", String(Math.floor(Date.now() / 1000) + 3600));

  await session.ensureSessionMigrated();
  const migrated = session.getStoredSessionEnvelope();

  assert.equal(migrated.session.token, "legacy-token");
  assert.ok(migrated.epoch);
  assert.equal(localStorage.getItem("moesekai-token"), null);
  await session.ensureSessionMigrated();
  assert.equal(session.getStoredSessionEnvelope().epoch, migrated.epoch);
});

test("login CAS and logout tombstones prevent stale identity writes", async () => {
  installBrowser();
  await session.ensureSessionMigrated();
  const initial = session.getStoredSessionEnvelope();
  const first = valid("ignored", "winner-token").session;
  const second = valid("ignored", "loser-token").session;

  const winner = await session.commitIdentitySession(first, initial);
  assert.equal(winner.session.token, "winner-token");
  assert.equal(await session.commitIdentitySession(second, initial), null);

  const beforeLogout = session.getStoredSessionEnvelope();
  assert.equal(await session.clearSession(beforeLogout), true);
  const tombstone = session.getStoredSessionEnvelope();
  assert.equal(tombstone.session, null);
  assert.notEqual(tombstone.epoch, beforeLogout.epoch);
  assert.equal(await session.commitIdentitySession(second, beforeLogout), null);
  assert.deepEqual(session.getStoredSessionEnvelope(), tombstone);
});

test("only the current token version can publish a same-role same-epoch refresh", async () => {
  installBrowser();
  await session.ensureSessionMigrated();
  const initial = session.getStoredSessionEnvelope();
  await session.commitIdentitySession(valid("ignored", "token-a").session, initial);
  const dispatched = session.getStoredSessionEnvelope();
  const replacement = valid("ignored", "token-b").session;

  const committed = await session.commitRefreshedSession(replacement, dispatched);
  assert.equal(committed.epoch, dispatched.epoch);
  assert.equal(committed.session.token, "token-b");
  assert.equal(await session.commitRefreshedSession(valid("ignored", "token-c").session, dispatched), null);
  assert.equal(session.getToken(), "token-b");
});

test("a verified same-username role change rotates the epoch and remounts stale admin UI", async () => {
  installBrowser();
  await session.ensureSessionMigrated();
  const initial = session.getStoredSessionEnvelope();
  await session.commitIdentitySession(valid("ignored", "admin-token", "admin").session, initial);
  const dispatched = session.getStoredSessionEnvelope();

  const demoted = await session.commitRefreshedSession(valid("ignored", "editor-token", "editor").session, dispatched);
  assert.equal(demoted.session.username, dispatched.session.username);
  assert.equal(demoted.session.role, "editor");
  assert.notEqual(demoted.epoch, dispatched.epoch);
  assert.equal(session.sameSessionIdentity(demoted, dispatched), false);
});

test("storage events propagate logout/account switches and invalid roles fail closed", () => {
  const { localStorage, window } = installBrowser();
  localStorage.setItem(session.SESSION_KEY, JSON.stringify(valid()));
  let changes = 0;
  const unsubscribe = session.subscribeSessionChanged(() => { changes++; });
  const viewer = JSON.stringify(valid("epoch-viewer", "viewer-token", "viewer"));
  localStorage.setItem(session.SESSION_KEY, viewer);
  window.dispatchEvent(new TestStorageEvent("storage", {
    key: session.SESSION_KEY,
    oldValue: JSON.stringify(valid()),
    newValue: viewer,
    storageArea: localStorage,
  }));

  assert.equal(changes, 1);
  assert.equal(session.getToken(), null);
  unsubscribe();
});

test("auth me initialization preserves the shared envelope on network and 5xx failures", async () => {
  for (const failure of [
    () => Promise.reject(new TypeError("network unavailable")),
    () => Promise.resolve(new Response('{"error":"temporary"}', {
      status: 503, headers: { "Content-Type": "application/json" },
    })),
  ]) {
    const expected = await installValidSession();
    Object.defineProperty(globalThis, "fetch", { configurable: true, value: failure });
    await assert.rejects(api.fetchMe());
    assert.deepEqual(session.getStoredSessionEnvelope(), expected);
  }
});

test("auth me initialization clears only the token-bound envelope on terminal 401", async () => {
  const expected = await installValidSession();
  Object.defineProperty(globalThis, "fetch", { configurable: true, value: async (_url, init) => {
    assert.equal(new Headers(init.headers).get("Authorization"), `Bearer ${expected.session.token}`);
    return new Response('{"error":"unauthorized"}', {
      status: 401, headers: { "Content-Type": "application/json" },
    });
  } });
  await assert.rejects(api.fetchMe(), error => error.status === 401);
  const tombstone = session.getStoredSessionEnvelope();
  assert.equal(tombstone.session, null);
  assert.notEqual(tombstone.epoch, expected.epoch);
});

test("refresh commits its valid successor across transient auth me failures", async () => {
  for (const meFailure of [
    () => Promise.reject(new TypeError("network unavailable")),
    () => Promise.resolve(new Response('{"error":"temporary"}', {
      status: 503, headers: { "Content-Type": "application/json" },
    })),
  ]) {
    const expected = await installValidSession();
    const expiresAt = Math.floor(Date.now() / 1000) + 7200;
    let calls = 0;
    Object.defineProperty(globalThis, "fetch", { configurable: true, value: async (_url, init) => {
      calls++;
      const authorization = new Headers(init.headers).get("Authorization");
      if (calls === 1) {
        assert.equal(authorization, `Bearer ${expected.session.token}`);
        return new Response(JSON.stringify({ token: "token-b", expiresAt }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      assert.equal(authorization, "Bearer token-b");
      return meFailure();
    } });

    assert.deepEqual(await api.refreshSession(), { token: "token-b", expiresAt });
    const stored = session.getStoredSessionEnvelope();
    assert.equal(stored.epoch, expected.epoch);
    assert.equal(stored.session.token, "token-b");
    assert.equal(stored.session.username, expected.session.username);
    assert.equal(calls, 2);
  }
});

test("explicit logout wins a refresh response race", async () => {
  const expected = await installValidSession();
  const expiresAt = Math.floor(Date.now() / 1000) + 7200;
  let signalRefreshDispatched;
  const refreshDispatched = new Promise((resolve) => { signalRefreshDispatched = resolve; });
  let releaseRefreshResponse;
  const refreshResponse = new Promise((resolve) => {
    releaseRefreshResponse = () => resolve(new Response(JSON.stringify({ token: "token-b", expiresAt }), {
      headers: { "Content-Type": "application/json" },
    }));
  });
  Object.defineProperty(globalThis, "fetch", { configurable: true, value: async (url) => {
    if (String(url).endsWith("/auth/refresh")) {
      signalRefreshDispatched();
      return refreshResponse;
    }
    return new Response(JSON.stringify({ username: expected.session.username, role: expected.session.role }), {
      headers: { "Content-Type": "application/json" },
    });
  } });

  const refresh = api.refreshSession();
  await refreshDispatched;

  // Reproduce the old shared-snapshot/exclusive-clear gap deterministically:
  // if logout takes a shared snapshot, allow refresh to commit before logout
  // receives that snapshot. The fixed logout has no shared phase to intercept.
  const baseLocks = navigator.locks;
  let armed = true;
  let interceptedSharedSnapshot = false;
  let releaseSharedSnapshot;
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: { locks: {
    request(name, options, callback) {
      if (armed && name === session.IDENTITY_LOCK && options?.mode === "shared") {
        armed = false;
        interceptedSharedSnapshot = true;
        const snapshot = callback();
        return new Promise((resolve) => { releaseSharedSnapshot = () => resolve(snapshot); });
      }
      return baseLocks.request(name, options, callback);
    },
  } } });

  const logout = session.clearSession();
  await new Promise((resolve) => setImmediate(resolve));
  if (interceptedSharedSnapshot) {
    releaseRefreshResponse();
    await refresh;
    releaseSharedSnapshot();
  } else {
    armed = false;
    assert.equal(await logout, true);
    releaseRefreshResponse();
    await assert.rejects(refresh, error => error.status === 409);
  }

  assert.equal(await logout, true);
  assert.equal(session.getStoredSessionEnvelope().session, null);
});

test("token-bound session cleanup retains compare-and-swap safety", async () => {
  const expected = await installValidSession();
  const replacement = valid("ignored", "token-b").session;
  const committed = await session.commitRefreshedSession(replacement, expected);

  assert.equal(await session.clearSession(expected), false);
  assert.deepEqual(session.getStoredSessionEnvelope(), committed);
});

test("terminal auth me rejection clears the committed refresh successor", async () => {
  await installValidSession();
  const expiresAt = Math.floor(Date.now() / 1000) + 7200;
  let calls = 0;
  Object.defineProperty(globalThis, "fetch", { configurable: true, value: async () => {
    calls++;
    if (calls === 1) {
      return new Response(JSON.stringify({ token: "token-b", expiresAt }), {
        headers: { "Content-Type": "application/json" },
      });
    }
    return new Response('{"error":"revoked"}', {
      status: 401, headers: { "Content-Type": "application/json" },
    });
  } });
  await assert.rejects(api.refreshSession(), error => error.status === 401);
  assert.equal(session.getStoredSessionEnvelope().session, null);
});

test("refresh verification rotates epoch for the same username when the authoritative role changes", async () => {
  const expected = await installValidSession();
  const expiresAt = Math.floor(Date.now() / 1000) + 7200;
  let calls = 0;
  Object.defineProperty(globalThis, "fetch", { configurable: true, value: async (_url, init) => {
    calls++;
    if (calls === 1) {
      assert.equal(new Headers(init.headers).get("Authorization"), `Bearer ${expected.session.token}`);
      return new Response(JSON.stringify({ token: "token-b", expiresAt }), {
        headers: { "Content-Type": "application/json" },
      });
    }
    return new Response(JSON.stringify({ username: expected.session.username, role: "admin" }), {
      headers: { "Content-Type": "application/json" },
    });
  } });

  await api.refreshSession();
  const verified = session.getStoredSessionEnvelope();
  assert.equal(verified.session.username, expected.session.username);
  assert.equal(verified.session.role, "admin");
  assert.notEqual(verified.epoch, expected.epoch);
  assert.equal(calls, 2);
});

test("strict editor writes require epoch-bound in-memory producer proof", async () => {
  const expected = await installValidSession();
  const status = {
    version: 1, instanceId: "cHJvZHVjZXItaW5zdGFuY2U", revision: 4,
    generation: 2, completedGeneration: 2, running: false, lastRun: "",
  };
  assert.equal(api.acceptLoadedProducerState(status), true);
  const calls = [];
  Object.defineProperty(globalThis, "fetch", { configurable: true, value: async (url, init) => {
    calls.push({ url, headers: new Headers(init.headers) });
    return new Response(JSON.stringify({ status: "ok", category: "cards", field: "prefix", key: "jp", text: "zh", source: "human" }), {
      headers: { "Content-Type": "application/json" },
    });
  } });
  await api.updateEntry("cards", "prefix", "jp", "zh", "human");
  assert.equal(calls[0].url, "/api/editor/v1/entry?response=correlated-v1");
  assert.equal(calls[0].headers.get("X-Moe-Loaded-Producer-State"), `${status.instanceId}:4:2`);
  assert.equal(calls[0].headers.get("Authorization"), `Bearer ${expected.session.token}`);

  let invalidations = 0;
  const unsubscribe = api.subscribeProducerProofInvalidated(() => { invalidations++; });
  Object.defineProperty(globalThis, "fetch", { configurable: true, value: async () =>
    new Response(JSON.stringify({ ...status, revision: 5 }), {
      status: 409, headers: { "Content-Type": "application/json" },
    }) });
  await assert.rejects(api.updateEntry("cards", "prefix", "jp", "stale", "human"), error =>
    error.status === 409 && error.code === "producer_state_changed" && error.producerStatus?.version === 1);
  assert.equal(invalidations, 1);

  assert.equal(api.acceptLoadedProducerState(status), true);
  Object.defineProperty(globalThis, "fetch", { configurable: true, value: async () =>
    new Response('{"error":"revision_conflict"}', {
      status: 409, headers: { "Content-Type": "application/json" },
    }) });
  await assert.rejects(api.updateEntry("cards", "prefix", "jp", "conflict", "human"), error =>
    error.status === 409 && error.code === "revision_conflict" && error.producerStatus === undefined);
  assert.equal(invalidations, 1);
  unsubscribe();

  assert.equal(api.acceptLoadedProducerState(status), true);
  await session.commitIdentitySession(valid("ignored", "other-token").session, expected);
  let fetched = false;
  Object.defineProperty(globalThis, "fetch", { configurable: true, value: async () => {
    fetched = true;
    return new Response('{"status":"ok"}', { headers: { "Content-Type": "application/json" } });
  } });
  await assert.rejects(api.updateEntry("cards", "prefix", "jp", "wrong identity", "human"), error => error.status === 409);
  assert.equal(fetched, false);
});

test("single source-review mutations reject unrelated 2xx responses and accept an exact idempotent retry", async () => {
  const expected = await installValidSession();
  const overallRequest = {
    reviewId: 17, gate: "overall", decision: "approved", expectedVersion: 4,
    idempotencyKey: "review-overall-retry-0001", note: "",
  };
  const validOverall = {
    reviewId: 17, state: "approved", identityGate: "approved", sourceUseGate: "approved",
    parseGate: "approved", version: 5, replayed: false,
  };
  const invalidOverall = [
    { ...validOverall, reviewId: 18 },
    { ...validOverall, version: 6 },
    { ...validOverall, state: "pending" },
    { ...validOverall, parseGate: "pending" },
    { ...validOverall, replayed: "false" },
    { ...validOverall, unrelated: true },
  ];
  const overallBodies = [];
  Object.defineProperty(globalThis, "fetch", { configurable: true, value: async (url, init) => {
    assert.equal(url, "/api/admin/lyrics-source-reviews/decision");
    assert.equal(new Headers(init.headers).get("Authorization"), `Bearer ${expected.session.token}`);
    overallBodies.push(JSON.parse(init.body));
    const response = invalidOverall.shift() ?? validOverall;
    return new Response(JSON.stringify(response), { headers: { "Content-Type": "application/json" } });
  } });
  for (let index = 0; index < 6; index++) {
    await assert.rejects(api.decideLyricsSourceReview(overallRequest), error =>
      error.status === 502 && error.code === "invalid_lyrics_source_review_response");
  }
  assert.deepEqual(await api.decideLyricsSourceReview(overallRequest), validOverall);
  assert.equal(overallBodies.every((body) => body.idempotencyKey === overallRequest.idempotencyKey), true);

  const candidateRequest = {
    reviewId: 23, exclude: false, expectedVersion: 8, idempotencyKey: "review-candidate-retry-001", note: "",
    candidateIdentity: { pageId: 12, title: "Song", canonicalUrl: "https://example.invalid/?oldid=34", revisionId: 34, sha1: "a".repeat(40), categories: [] },
  };
  const validCandidate = {
    reviewId: 23, state: "approved", identityGate: "not_applicable", sourceUseGate: "not_applicable",
    parseGate: "not_applicable", version: 9, replayed: true,
  };
  const candidateBodies = [];
  let candidateAttempt = 0;
  Object.defineProperty(globalThis, "fetch", { configurable: true, value: async (url, init) => {
    assert.equal(url, "/api/admin/lyrics-source-reviews/candidate-selection");
    candidateBodies.push(JSON.parse(init.body));
    candidateAttempt++;
    const response = candidateAttempt === 1 ? { ...validCandidate, identityGate: "approved" } : validCandidate;
    return new Response(JSON.stringify(response), { headers: { "Content-Type": "application/json" } });
  } });
  await assert.rejects(api.selectLyricsSourceCandidate(candidateRequest), error =>
    error.status === 502 && error.code === "invalid_lyrics_source_review_response");
  assert.deepEqual(await api.selectLyricsSourceCandidate(candidateRequest), validCandidate);
  assert.equal(candidateBodies.every((body) => body.idempotencyKey === candidateRequest.idempotencyKey), true);

  const excludeRequest = {
    reviewId: 24, exclude: true, expectedVersion: 2, idempotencyKey: "review-candidate-exclude-001", note: "",
  };
  const validExcluded = {
    reviewId: 24, state: "rejected", identityGate: "not_applicable", sourceUseGate: "not_applicable",
    parseGate: "not_applicable", version: 3, replayed: false,
  };
  let excludeAttempt = 0;
  Object.defineProperty(globalThis, "fetch", { configurable: true, value: async () => {
    excludeAttempt++;
    const response = excludeAttempt === 1 ? { ...validExcluded, state: "approved" } : validExcluded;
    return new Response(JSON.stringify(response), { headers: { "Content-Type": "application/json" } });
  } });
  await assert.rejects(api.selectLyricsSourceCandidate(excludeRequest), error =>
    error.status === 502 && error.code === "invalid_lyrics_source_review_response");
  assert.deepEqual(await api.selectLyricsSourceCandidate(excludeRequest), validExcluded);
});
