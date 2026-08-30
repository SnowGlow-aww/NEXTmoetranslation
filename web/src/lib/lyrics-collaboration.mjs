const STABLE_RENDITION_KEY = /^[a-z0-9][a-z0-9._-]{0,127}$/;
const TARGET_SIDES = new Set(["full", "game", "credits"]);

function record(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

/**
 * Parse the additive lyrics.updated collaboration payload. Legacy events and
 * older source-v3 events without editionKey intentionally degrade to
 * song-level handling so a shared global revision is never ignored.
 */
export function normalizeLyricsUpdateEvent(value) {
  if (!record(value)) return null;
  const musicId = Number(value.musicId);
  const revision = Number(value.revision);
  if (!Number.isSafeInteger(musicId) || musicId <= 0 || !Number.isSafeInteger(revision) || revision <= 0) return null;
  const clientId = typeof value.clientId === "string" ? value.clientId : "";
  const targetFields = [value.editionKey, value.renditionKey, value.side, value.locale];
  const hasTarget = targetFields.some((field) => field !== undefined);
  if (!hasTarget) return { musicId, revision, clientId };
  if (typeof value.editionKey !== "string" || !STABLE_RENDITION_KEY.test(value.editionKey)) {
    return { musicId, revision, clientId };
  }
  const hasRenditionTarget = [value.renditionKey, value.side, value.locale].some((field) => field !== undefined);
  if (!hasRenditionTarget) return { musicId, revision, clientId, editionKey: value.editionKey };
  if (typeof value.renditionKey !== "string" || !STABLE_RENDITION_KEY.test(value.renditionKey) ||
      typeof value.side !== "string" || !TARGET_SIDES.has(value.side) || value.locale !== "zh-CN") {
    return { musicId, revision, clientId };
  }
  return {
    musicId,
    revision,
    clientId,
    editionKey: value.editionKey,
    renditionKey: value.renditionKey,
    side: value.side,
    locale: "zh-CN",
  };
}

/** Song-level events match every editor target for that song. */
export function lyricsUpdateMatchesEditorTarget(update, target) {
  if (!record(update) || !record(target) || update.musicId !== target.musicId) return false;
  if (!update.editionKey) return true;
  if (update.editionKey !== target.editionKey) return false;
  if (!update.renditionKey || !update.side || !update.locale) return true;
  if (update.renditionKey !== target.renditionKey || update.locale !== target.locale) return false;
  if (update.side === "credits" || update.side === target.side) return true;
  return target.side === "game" && target.projectionKind === "exact_projection" && update.side === "full";
}

export function lyricsUpdateTargetLabel(update) {
  if (!record(update) || !update.editionKey) return "";
  if (!update.renditionKey || !update.side || !update.locale) return `${update.editionKey} 译本`;
  const side = update.side === "credits" ? "翻译/校对署名" : update.side === "full" ? "Full 简中" : "Game 简中";
  return `${update.editionKey} · ${update.renditionKey} · ${side}`;
}
