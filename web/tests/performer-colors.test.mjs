import assert from "node:assert/strict";
import test from "node:test";
import {
  OFFICIAL_PERFORMER_COLORS,
  OFFICIAL_UNIT_COLORS,
  performerRepresentativeColor,
  unitRepresentativeColor,
} from "../src/lib/performer-colors.mjs";

test("official Project SEKAI lyrics colours override Wiki approximations", () => {
  assert.equal(performerRepresentativeColor("meiko", "25-ji, Nightcord de. Version", "#DE4444"), "#DD4444");
  assert.equal(performerRepresentativeColor("ena", "25-ji, Nightcord de. Version", "#CCAA87"), "#CCAA88");
  assert.equal(performerRepresentativeColor("mafuyu", "25-ji, Nightcord de. Version", "#8889CC"), "#8888CC");
  assert.equal(performerRepresentativeColor("kanade", "25-ji, Nightcord de. Version", "#BB6588"), "#BB6688");
  assert.equal(performerRepresentativeColor("mizuki", "25-ji, Nightcord de. Version", "#E4A8CA"), "#DDAACC");
});

test("official colour lookup supports localized aliases and unit chorus fallback", () => {
  assert.equal(performerRepresentativeColor("東雲絵名"), OFFICIAL_PERFORMER_COLORS.ena);
  assert.equal(performerRepresentativeColor("初音ミク"), OFFICIAL_PERFORMER_COLORS.miku);
  assert.equal(unitRepresentativeColor("25-ji, Nightcord de. Version"), OFFICIAL_UNIT_COLORS.n25);
  assert.equal(performerRepresentativeColor("chorus", "25-ji, Nightcord de. Version"), OFFICIAL_UNIT_COLORS.n25);
});

test("unknown source-local identities retain their validated source colour", () => {
  assert.equal(performerRepresentativeColor("guest", "Original Version", "#123456"), "#123456");
  assert.equal(performerRepresentativeColor("guest", "Original Version"), undefined);
});
