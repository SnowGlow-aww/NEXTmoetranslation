import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

class FakeElement {
  constructor(kind = "plain") { this.kind = kind; }
  closest(selector) {
    if (this.kind === "editable" && selector.includes("input")) return this;
    if (this.kind === "interactive" && selector.includes("button")) return this;
    if (this.kind === "role" && selector.includes("[role='button']")) return this;
    return null;
  }
}
globalThis.Element = FakeElement;

const { lyricsReviewShortcutAction } = await import("../src/lib/lyrics-review-shortcuts.mjs");

const keyboard = (overrides = {}) => ({
  key: "", code: "", metaKey: false, ctrlKey: false, altKey: false, shiftKey: false,
  defaultPrevented: false, repeat: false, isComposing: false, target: new FakeElement(false),
  ...overrides,
});
const ready = { busy: false, modalOpen: false, submitting: false, confirmEligible: false };
const modalReady = { busy: false, modalOpen: true, submitting: false, confirmEligible: true };

test("lyrics review shortcuts map the fixed navigation, selection, and confirmation keys", () => {
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "ArrowUp", metaKey: true }), ready), "previous");
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "ArrowDown", ctrlKey: true }), ready), "next");
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: " ", code: "Space" }), ready), "toggle-active");
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "a", ctrlKey: true }), ready), "toggle-all");
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "A", shiftKey: true }), ready), "approve");
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "r", shiftKey: true }), ready), "reject");
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "Escape" }), ready), "clear-selection");
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "Enter" }), modalReady), "confirm");
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "Escape" }), modalReady), "close-modal");
});

test("modal mode blocks list shortcuts, prioritizes Escape, and preserves native Enter controls", () => {
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "a", ctrlKey: true }), modalReady), null);
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "A", shiftKey: true }), modalReady), null);
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "Escape" }), modalReady), "close-modal");
  for (const kind of ["interactive", "editable", "role"]) {
    assert.equal(lyricsReviewShortcutAction(keyboard({ key: "Enter", target: new FakeElement(kind) }), modalReady), null);
  }
});

test("write lock still permits modal Escape, while submit blocks Escape and ineligible Enter", () => {
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "Escape" }), {
    busy: true, modalOpen: true, submitting: false, confirmEligible: false,
  }), "close-modal");
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "Escape" }), {
    busy: true, modalOpen: true, submitting: true, confirmEligible: false,
  }), null);
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "Enter" }), {
    busy: false, modalOpen: true, submitting: false, confirmEligible: false,
  }), null);
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "Enter" }), {
    busy: true, modalOpen: true, submitting: false, confirmEligible: true,
  }), null);
});

test("editable, interactive, IME, repeat, prevented, and busy events are guarded", () => {
  for (const event of [
    keyboard({ key: "a", ctrlKey: true, target: new FakeElement("interactive") }),
    keyboard({ key: "a", ctrlKey: true, target: new FakeElement("editable") }),
    keyboard({ key: "a", ctrlKey: true, isComposing: true }),
    keyboard({ key: "a", ctrlKey: true, repeat: true }),
    keyboard({ key: "a", ctrlKey: true, defaultPrevented: true }),
  ]) assert.equal(lyricsReviewShortcutAction(event, ready), null);
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "a", ctrlKey: true }), { busy: true, modalOpen: false }), null);
});

test("unsupported modifier combinations keep browser defaults", () => {
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "Enter", ctrlKey: true }), modalReady), null);
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "Escape", altKey: true }), modalReady), null);
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "a", ctrlKey: true, shiftKey: true }), ready), null);
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "ArrowDown", ctrlKey: true, altKey: true }), ready), null);
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: " ", code: "Space", shiftKey: true }), ready), null);
});

test("cancel and close controls never map Enter to global confirmation", () => {
  for (const target of [new FakeElement("interactive"), new FakeElement("role")]) {
    assert.equal(lyricsReviewShortcutAction(keyboard({ key: "Enter", target }), modalReady), null);
  }
  assert.equal(lyricsReviewShortcutAction(keyboard({ key: "Enter" }), modalReady), "confirm");
});

test("shortcut declarations require the modal submission and confirmation guards", async () => {
  const declaration = await readFile(new URL("../src/lib/lyrics-review-shortcuts.d.ts", import.meta.url), "utf8");
  const context = declaration.slice(declaration.indexOf("export interface LyricsReviewShortcutContext"), declaration.indexOf("export function isLyricsReviewInteractiveTarget"));
  for (const field of ["busy: boolean", "modalOpen: boolean", "submitting: boolean", "confirmEligible: boolean"]) {
    assert.ok(context.includes(field), `missing shortcut context field ${field}`);
  }
});
