import assert from "node:assert/strict";
import test from "node:test";

import {
  lyricsUpdateMatchesEditorTarget,
  lyricsUpdateTargetLabel,
  normalizeLyricsUpdateEvent,
} from "../src/lib/lyrics-collaboration.mjs";

const activeGame = {
  musicId: 765,
  editionKey: "main",
  renditionKey: "sekai",
  side: "game",
  locale: "zh-CN",
};

test("lyrics collaboration parses exact single targets and preserves song-level fallback", () => {
  assert.deepEqual(normalizeLyricsUpdateEvent({
    musicId: 765,
    revision: 4,
    clientId: "peer-tab",
    editionKey: "main",
    renditionKey: "sekai",
    side: "game",
    locale: "zh-CN",
  }), {
    musicId: 765,
    revision: 4,
    clientId: "peer-tab",
    editionKey: "main",
    renditionKey: "sekai",
    side: "game",
    locale: "zh-CN",
  });
  assert.deepEqual(normalizeLyricsUpdateEvent({ musicId: 765, revision: 5, clientId: "peer-tab" }), {
    musicId: 765,
    revision: 5,
    clientId: "peer-tab",
  });
  assert.deepEqual(normalizeLyricsUpdateEvent({
    musicId: 765, revision: 6, clientId: "old-server", renditionKey: "sekai", side: "game", locale: "zh-CN",
  }), {
    musicId: 765, revision: 6, clientId: "old-server",
  }, "old SSE without editionKey must safely degrade to song-level");
  assert.deepEqual(normalizeLyricsUpdateEvent({
    musicId: 765,
    revision: 6,
    clientId: "peer-tab",
    renditionKey: "SEKAI",
    side: "game",
    locale: "zh-CN",
  }), {
    musicId: 765,
    revision: 6,
    clientId: "peer-tab",
  }, "malformed additive targets must fail closed to song-level handling");
  assert.equal(normalizeLyricsUpdateEvent({ musicId: 0, revision: 1 }), null);
});

test("lyrics collaboration distinguishes the active stable key and side without ignoring shared revisions", () => {
  const active = normalizeLyricsUpdateEvent({
    musicId: 765, revision: 2, clientId: "peer", editionKey: "main", renditionKey: "sekai", side: "game", locale: "zh-CN",
  });
  const otherSide = normalizeLyricsUpdateEvent({
    musicId: 765, revision: 3, clientId: "peer", editionKey: "main", renditionKey: "sekai", side: "full", locale: "zh-CN",
  });
  const credits = normalizeLyricsUpdateEvent({
    musicId: 765, revision: 4, clientId: "peer", editionKey: "main", renditionKey: "sekai", side: "credits", locale: "zh-CN",
  });
  const otherFamily = normalizeLyricsUpdateEvent({
    musicId: 765, revision: 5, clientId: "peer", editionKey: "main", renditionKey: "virtual-singer", side: "game", locale: "zh-CN",
  });
  const otherEdition = normalizeLyricsUpdateEvent({
    musicId: 765, revision: 6, clientId: "peer", editionKey: "literal", renditionKey: "sekai", side: "game", locale: "zh-CN",
  });
  const editionLevel = normalizeLyricsUpdateEvent({ musicId: 765, revision: 7, clientId: "peer", editionKey: "main" });
  const songLevel = normalizeLyricsUpdateEvent({ musicId: 765, revision: 8, clientId: "peer" });

  assert.equal(lyricsUpdateMatchesEditorTarget(active, activeGame), true);
  assert.equal(lyricsUpdateMatchesEditorTarget(otherSide, activeGame), false);
  assert.equal(lyricsUpdateMatchesEditorTarget(otherSide, { ...activeGame, projectionKind: "exact_projection" }), true,
    "a Full localization mutation changes the currently projected Game side");
  assert.equal(lyricsUpdateMatchesEditorTarget(otherSide, { ...activeGame, projectionKind: "independent_game" }), false);
  assert.equal(lyricsUpdateMatchesEditorTarget(credits, activeGame), true);
  assert.equal(lyricsUpdateMatchesEditorTarget(otherFamily, activeGame), false);
  assert.equal(lyricsUpdateMatchesEditorTarget(otherEdition, activeGame), false);
  assert.equal(lyricsUpdateMatchesEditorTarget(editionLevel, activeGame), true);
  assert.equal(lyricsUpdateMatchesEditorTarget(songLevel, activeGame), true);
  assert.equal(lyricsUpdateMatchesEditorTarget(songLevel, { ...activeGame, musicId: 764 }), false);

  assert.equal(lyricsUpdateTargetLabel(active), "main · sekai · Game 简中");
  assert.equal(lyricsUpdateTargetLabel(credits), "main · sekai · 翻译/校对署名");
  assert.equal(lyricsUpdateTargetLabel(editionLevel), "main 译本");
  assert.equal(lyricsUpdateTargetLabel(songLevel), "");
});
