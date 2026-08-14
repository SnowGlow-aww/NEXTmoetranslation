export const TRANSLATION_EDITION_KEY_PATTERN = /^[a-z0-9][a-z0-9._-]{0,127}$/;

function record(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function isTranslationEditionKey(value) {
  return typeof value === "string" && TRANSLATION_EDITION_KEY_PATTERN.test(value);
}

function isWellFormedUnicode(value) {
  for (let index = 0; index < value.length; index++) {
    const unit = value.charCodeAt(index);
    if (unit >= 0xd800 && unit <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false;
      index++;
    } else if (unit >= 0xdc00 && unit <= 0xdfff) {
      return false;
    }
  }
  return true;
}

export function isTranslationEditionLabel(value) {
  return typeof value === "string" && value.length > 0 && value === value.trim() &&
    isWellFormedUnicode(value) && new TextEncoder().encode(value).length <= 256;
}

export function validateTranslationEditionSummaries(value, path = "translationEditions") {
  const details = [];
  if (!Array.isArray(value) || value.length === 0 || value.length > 16) {
    return { ok: false, details: [`${path} must contain 1-16 edition summaries`] };
  }
  const keys = new Set();
  const summaries = [];
  for (let index = 0; index < value.length; index++) {
    const summary = value[index];
    const itemPath = `${path}[${index}]`;
    if (!record(summary)) {
      details.push(`${itemPath} must be an object`);
      continue;
    }
    for (const key of Object.keys(summary)) {
      if (key !== "key" && key !== "label") details.push(`${itemPath}.${key} is not allowed`);
    }
    if (!isTranslationEditionKey(summary.key)) {
      details.push(`${itemPath}.key is invalid`);
    } else if (keys.has(summary.key)) {
      details.push(`${path} repeats edition key ${summary.key}`);
    } else {
      keys.add(summary.key);
    }
    if (!isTranslationEditionLabel(summary.label)) details.push(`${itemPath}.label must be trim-stable UTF-8 between 1 and 256 bytes`);
    summaries.push({ key: summary.key, label: summary.label });
  }
  return details.length > 0 ? { ok: false, details } : { ok: true, value: summaries };
}

export function selectTranslationEditionKey(requestedKey, defaultKey, summaries) {
  const validated = validateTranslationEditionSummaries(summaries);
  if (!validated.ok) return "";
  const available = new Set(validated.value.map((summary) => summary.key));
  if (isTranslationEditionKey(requestedKey) && available.has(requestedKey)) return requestedKey;
  if (isTranslationEditionKey(defaultKey) && available.has(defaultKey)) return defaultKey;
  return validated.value[0].key;
}

export function translationEditionURLHint(search) {
  let params;
  try {
    params = search instanceof URLSearchParams ? search : new URLSearchParams(typeof search === "string" ? search : "");
  } catch {
    return "";
  }
  const hinted = params.get("edition") || "";
  return isTranslationEditionKey(hinted) ? hinted : "";
}

export function renameTranslationEditionSummaries(summaries, editionKey, label) {
  const validated = validateTranslationEditionSummaries(summaries);
  if (!validated.ok) throw new TypeError(validated.details.join("; "));
  if (!isTranslationEditionKey(editionKey)) throw new TypeError("editionKey is invalid");
  if (!isTranslationEditionLabel(label)) throw new TypeError("label must be trim-stable UTF-8 between 1 and 256 bytes");
  if (!validated.value.some((summary) => summary.key === editionKey)) throw new TypeError("editionKey does not exist");
  return validated.value.map((summary) => summary.key === editionKey ? { key: summary.key, label } : { ...summary });
}
