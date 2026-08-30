import { test } from "node:test";
import assert from "node:assert/strict";
import {
  getMusicVocalAudioUrl,
  getCharacterIconUrl,
  getCharacterLabel,
  GAME_CHARACTER_NAMES,
} from "../src/lib/music-vocals.mjs";

test("getMusicVocalAudioUrl constructs exact long audio CDN URL", () => {
  const url = getMusicVocalAudioUrl("0050_01");
  assert.equal(url, "https://storage.exmeaning.com/sekai-jp-assets/music/long/0050_01/0050_01.mp3");
});

test("getCharacterIconUrl constructs exact character avatar CDN URL", () => {
  assert.equal(getCharacterIconUrl(1), "https://moe.exmeaning.com/assets/chr_ts_1.png");
  assert.equal(getCharacterIconUrl(26), "https://moe.exmeaning.com/assets/chr_ts_26.png");
});

test("getCharacterLabel maps game characters and outside characters", () => {
  const ichika = {
    id: 1,
    musicId: 50,
    musicVocalId: 46,
    characterType: "game_character",
    characterId: 1,
    seq: 1,
  };
  assert.equal(getCharacterLabel(ichika, {}), "星乃 一歌");

  const outsideChar = {
    id: 2,
    musicId: 50,
    musicVocalId: 1654,
    characterType: "outside_character",
    characterId: 30,
    seq: 2,
  };
  assert.equal(getCharacterLabel(outsideChar, { 30: "天祥院 英智" }), "天祥院 英智");
  assert.equal(getCharacterLabel(outsideChar, {}), "嘉宾 30");
});

test("GAME_CHARACTER_NAMES covers all 26 official characters", () => {
  for (let i = 1; i <= 26; i++) {
    assert.ok(GAME_CHARACTER_NAMES[i], `Character ID ${i} must have a name`);
  }
});
