const CANONICAL_SOURCE_PERFORMERS = Object.freeze({
  ichika: 1, saki: 2, honami: 3, shiho: 4,
  minori: 5, haruka: 6, airi: 7, shizuku: 8,
  kohane: 9, an: 10, akito: 11, toya: 12,
  tsukasa: 13, emu: 14, nene: 15, rui: 16,
  kanade: 17, mafuyu: 18, ena: 19, mizuki: 20,
  miku: 21, hatsunemiku: 21, rin: 22, kagaminerin: 22,
  len: 23, kagaminelen: 23, luka: 24, megurineluka: 24,
  meiko: 25, kaito: 26,
});

function normalizeAlias(value) {
  return typeof value === "string"
    ? value.normalize("NFKC").trim().toLocaleLowerCase("en-US").replace(/[^\p{L}\p{N}]+/gu, "")
    : "";
}

function addUniqueAlias(aliases, alias, performerId) {
  if (!alias) return;
  if (!aliases.has(alias)) aliases.set(alias, performerId);
  else if (aliases.get(alias) !== performerId) aliases.set(alias, null);
}

function performerAliases(performers) {
  const validIds = new Set();
  const aliases = new Map();
  for (const performer of Array.isArray(performers) ? performers : []) {
    if (!Number.isSafeInteger(performer?.performerId) || performer.performerId <= 0) continue;
    validIds.add(performer.performerId);
    for (const name of Object.values(performer.name || {})) {
      addUniqueAlias(aliases, normalizeAlias(name), performer.performerId);
    }
  }
  for (const [alias, performerId] of Object.entries(CANONICAL_SOURCE_PERFORMERS)) {
    if (validIds.has(performerId)) addUniqueAlias(aliases, alias, performerId);
  }
  return { aliases, validIds };
}

function mapSourcePerformerIds(sourceIds, mapping, unmapped) {
  if (!Array.isArray(sourceIds)) return null;
  const mapped = new Set();
  for (const sourceId of sourceIds) {
    if (typeof sourceId !== "string") {
      unmapped.add(String(sourceId));
      continue;
    }
    const normalized = normalizeAlias(sourceId);
    let performerId = null;
    if (/^[1-9]\d*$/.test(normalized)) {
      const numeric = Number(normalized);
      if (Number.isSafeInteger(numeric) && mapping.validIds.has(numeric)) performerId = numeric;
    }
    if (performerId == null) performerId = mapping.aliases.get(normalized) ?? null;
    if (!Number.isSafeInteger(performerId) || performerId <= 0) {
      unmapped.add(sourceId);
      continue;
    }
    mapped.add(performerId);
  }
  return [...mapped].sort((left, right) => left - right);
}

function cloneRuby(ruby, text) {
  if (!Array.isArray(ruby) || ruby.length === 0) return null;
  const cloned = [];
  for (const span of ruby) {
    if (!span || typeof span.text !== "string" || span.text === "" ||
        (span.reading !== undefined && typeof span.reading !== "string")) return null;
    cloned.push({ text: span.text, ...(span.reading ? { reading: span.reading } : {}) });
  }
  return cloned.map((span) => span.text).join("") === text ? cloned : null;
}

export function buildLyricsLinesFromSourcePreview(preview, performers) {
  if (!preview || !Array.isArray(preview.lines)) {
    return { ok: false, code: "invalid_source_preview", details: ["来源预览缺少歌词行"] };
  }
  const structured = preview.structuredLines;
  if (structured !== undefined && (!Array.isArray(structured) || structured.length !== preview.lines.length)) {
    return { ok: false, code: "invalid_source_preview", details: ["结构化来源证据与歌词行数量不一致"] };
  }
  const mapping = performerAliases(performers);
  const unmapped = new Set();
  const lines = [];
  for (let order = 0; order < preview.lines.length; order++) {
    const line = preview.lines[order];
    if (!line || typeof line.japanese !== "string" || line.japanese === "") {
      return { ok: false, code: "invalid_source_preview", details: [`第 ${order + 1} 行来源原文无效`] };
    }
    const structuredLine = structured?.[order];
    let segments;
    if (structuredLine) {
      if (structuredLine.japanese !== line.japanese ||
          Boolean(structuredLine.stanzaBreakBefore) !== Boolean(line.stanzaBreakBefore) ||
          !Array.isArray(structuredLine.segments) || structuredLine.segments.length === 0) {
        return { ok: false, code: "invalid_source_preview", details: [`第 ${order + 1} 行结构化来源证据与原文不一致`] };
      }
      const trailing = mapSourcePerformerIds(structuredLine.trailingPerformerIds, mapping, unmapped);
      if (trailing == null) {
        return { ok: false, code: "invalid_source_preview", details: [`第 ${order + 1} 行行末演唱者证据格式无效`] };
      }
      segments = [];
      for (let segmentIndex = 0; segmentIndex < structuredLine.segments.length; segmentIndex++) {
        const segment = structuredLine.segments[segmentIndex];
        if (!segment || typeof segment.text !== "string" || segment.text === "") {
          return { ok: false, code: "invalid_source_preview", details: [`第 ${order + 1} 行第 ${segmentIndex + 1} 分段无效`] };
        }
        const ruby = cloneRuby(segment.ruby, segment.text);
        const mapped = mapSourcePerformerIds(segment.performerIds, mapping, unmapped);
        if (!ruby || mapped == null) {
          return { ok: false, code: "invalid_source_preview", details: [`第 ${order + 1} 行第 ${segmentIndex + 1} 分段证据无效`] };
        }
        segments.push({ text: segment.text, performerIds: mapped.length > 0 ? mapped : [...trailing], ruby });
      }
      if (segments.map((segment) => segment.text).join("") !== line.japanese) {
        return { ok: false, code: "invalid_source_preview", details: [`第 ${order + 1} 行分段未完整拼接为来源原文`] };
      }
    } else {
      segments = [{ text: line.japanese, performerIds: [], ruby: [{ text: line.japanese }] }];
    }
    lines.push({
      id: `source-${order + 1}`,
      order,
      japanese: line.japanese,
      "zh-CN": "",
      "en-US": "",
      ...(line.stanzaBreakBefore ? { stanzaBreakBefore: true } : {}),
      segments,
    });
  }
  if (unmapped.size > 0) {
    return {
      ok: false,
      code: "source_performer_mapping_failed",
      details: [`无法把来源演唱者标识映射到当前角色目录：${[...unmapped].join("、")}`],
      unmappedIds: [...unmapped],
    };
  }
  return { ok: true, lines };
}
