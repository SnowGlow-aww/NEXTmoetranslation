import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const themes = {
  light: {
    surfaces: ["#f5f4ef", "#ffffff", "#faf9f5", "#f0eee7"],
    surface: "#ffffff",
    text: ["#1f1e1b", "#6b6960", "#6f6c64", "#ad5032", "#447453", "#ac513d", "#85601f"],
    sourceText: ["#447453", "#ad5032", "#775bba", "#376fa8", "#6f6c64"],
    statusText: ["#ad5032", "#447453", "#85601f"],
    error: "#ac513d",
    controlBorder: "#8d8980",
    focus: { accent: "#ad5032", text: "#1f1e1b" },
    primary: { background: "#ad5032", foreground: "#ffffff" },
    active: { background: "#f6ebe6", foreground: "#ad5032", secondary: "#6b6960" },
    review: { surfaces: ["#f7f7fb", "#ffffff", "#f2f1f8", "#eceaf4", "#efedf7"], text: "#1f1e1b", secondary: "#6b6960", accent: "#665d86" },
  },
  dark: {
    surfaces: ["#1f1e1b", "#2a2925", "#262521", "#33312c"],
    surface: "#2a2925",
    text: ["#ecebe5", "#b8b4aa", "#a09d93", "#e07e58", "#74ad82", "#e08470", "#d4a85e"],
    sourceText: ["#74ad82", "#d97a55", "#a58be6", "#69a5df", "#a09d93"],
    statusText: ["#e07e58", "#74ad82", "#d4a85e"],
    error: "#e08470",
    controlBorder: "#817d73",
    focus: { accent: "#e07e58", text: "#ecebe5" },
    primary: { background: "#e07e58", foreground: "#1f1e1b" },
    active: { background: "#3a2c25", foreground: "#e07e58", secondary: "#b8b4aa" },
    review: { surfaces: ["#202026", "#292830", "#25242c", "#31303a", "#373342"], text: "#ecebe5", secondary: "#b8b4aa", accent: "#c1b6e5" },
  },
};

function rgb(hex) {
  return [1, 3, 5].map((offset) => Number.parseInt(hex.slice(offset, offset + 2), 16) / 255);
}

function luminance(color) {
  const channel = (value) => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  const [red, green, blue] = color;
  return 0.2126 * channel(red) + 0.7152 * channel(green) + 0.0722 * channel(blue);
}

function contrast(left, right) {
  const values = [luminance(rgb(left)), luminance(rgb(right))].sort((a, b) => b - a);
  return (values[0] + 0.05) / (values[1] + 0.05);
}

function mix(left, right, leftWeight) {
  const leftColor = rgb(left);
  const rightColor = rgb(right);
  return `#${leftColor.map((channel, index) => Math.round((leftWeight * channel + (1 - leftWeight) * rightColor[index]) * 255)
    .toString(16).padStart(2, "0")).join("")}`;
}

function assertContrast(theme, label, foreground, background, minimum) {
  const actual = contrast(foreground, background);
  assert.ok(actual >= minimum,
    `${theme} ${label} ${foreground} on ${background} has contrast ${actual.toFixed(2)}`);
}

test("light and dark small text and semantic colors meet WCAG AA on representative surfaces", () => {
  for (const [theme, palette] of Object.entries(themes)) {
    for (const foreground of palette.text) {
      for (const surface of palette.surfaces) {
        assertContrast(theme, "text", foreground, surface, 4.5);
      }
    }
  }
});

test("lyrics review text and performer P-codes stay readable on Direction B surfaces", () => {
  for (const [theme, palette] of Object.entries(themes)) {
    for (const surface of palette.review.surfaces) {
      assertContrast(theme, "review lyrics text", palette.review.text, surface, 4.5);
      assertContrast(theme, "review performer P-code", palette.review.secondary, surface, 4.5);
      assertContrast(theme, "review accent label", palette.review.accent, surface, 4.5);
    }
  }
  assert.ok(contrast("#DDAACC", "#ffffff") < 4.5, "official performer pink must remain a swatch, not light-theme foreground text");
});

test("source tags and status washes keep 10-11px text readable on their actual surfaces", () => {
  for (const [theme, palette] of Object.entries(themes)) {
    for (const foreground of palette.sourceText) {
      const background = mix(foreground, palette.surface, 0.04);
      assertContrast(theme, "source tag", foreground, background, 4.5);
    }
    for (const foreground of palette.statusText) {
      const background = mix(foreground, palette.surface, 0.05);
      assertContrast(theme, "status chip", foreground, background, 4.5);
    }
    assertContrast(theme, "error panel", palette.error, mix(palette.error, palette.surface, 0.07), 4.5);
    assertContrast(theme, "active navigation", palette.active.foreground, palette.active.background, 4.5);
    assertContrast(theme, "active navigation metadata", palette.active.secondary, palette.active.background, 4.5);
  }
});

test("translation-edition menu inherits warm neutral, dark, focus, and mobile tokens", async () => {
  const css = await readFile(new URL("../src/app/globals.css", import.meta.url), "utf8");
  const editionCSS = css.split(".lyrics-edition-selector")[1].split(".lyrics-error")[0];
  for (const token of ["var(--surface)", "var(--surface-2)", "var(--surface-3)", "var(--border)", "var(--border-strong)", "var(--text)", "var(--text-secondary)", "var(--text-dim)", "var(--accent)"]) {
    assert.ok(editionCSS.includes(token), `edition menu must use theme token ${token}`);
  }
  assert.doesNotMatch(editionCSS, /#[0-9a-f]{3,8}/i);
  assert.match(css, /:focus-visible \{[\s\S]*outline: 2px solid color-mix/);
  assert.match(css, /@media \(max-width: 768px\)[\s\S]*lyrics-edition-trigger[\s\S]*min-height: 44px/);
});

test("primary-button text, control boundaries, and focus indicators remain visible", () => {
  for (const [theme, palette] of Object.entries(themes)) {
    assertContrast(theme, "primary button", palette.primary.foreground, palette.primary.background, 4.5);

    for (const surface of palette.surfaces.slice(1)) {
      assertContrast(theme, "control border", palette.controlBorder, surface, 3);
    }

    const focus = mix(palette.focus.accent, palette.focus.text, 0.54);
    for (const surface of palette.surfaces) {
      assertContrast(theme, "focus ring", focus, surface, 3);
    }
  }
});
