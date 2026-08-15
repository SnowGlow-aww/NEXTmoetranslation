export interface EventStoryEntryIdentity {
  key: string;
  segmentId?: string;
  sourceHash?: string;
  episodeNo?: string;
  entryType?: "title" | "talk";
  japanese?: string;
}

export interface EventStoryDraftTranslation {
  segmentId: string;
  authoritativeText: string;
}

export interface EventStoryUpdateIdentity {
  segmentId: string;
  episodeNo: string;
  jpKey: string;
  entryType: string;
}

const EVENT_STORY_TITLE_MARKER = "__title__";

export function eventStoryEpisodeNo(entry: EventStoryEntryIdentity): string {
  const explicit = entry.episodeNo?.trim();
  if (explicit) return explicit;
  const separator = entry.key.indexOf("|");
  const encoded = (separator >= 0 ? entry.key.slice(0, separator) : "").trim();
  return encoded || "1";
}

export function eventStoryEntryType(entry: EventStoryEntryIdentity): "title" | "talk" {
  if (entry.entryType) return entry.entryType;
  return entry.key.split("|")[1] === EVENT_STORY_TITLE_MARKER ? "title" : "talk";
}

export function eventStoryEntryHasCanonicalIdentity(entry: EventStoryEntryIdentity): boolean {
  return Boolean(entry.segmentId?.trim() && entry.sourceHash?.trim());
}

export function restoreEventStoryDraftEntries<T extends EventStoryEntryIdentity & { text: string }>(
  entries: readonly T[],
  translations: readonly EventStoryDraftTranslation[],
): T[] {
  const authoritative = new Map(translations.map((translation) => [translation.segmentId, translation.authoritativeText]));
  return entries.map((entry) => entry.segmentId && authoritative.has(entry.segmentId)
    ? { ...entry, text: authoritative.get(entry.segmentId) as string }
    : entry);
}

export function listEventStoryEpisodeNos(entries: readonly EventStoryEntryIdentity[]): string[] {
  return [...new Set(entries.map(eventStoryEpisodeNo))]
    .sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
}

export function resolveSelectedEventStoryEpisode(selected: string, available: readonly string[]): string {
  if (available.length === 0) return "all";
  if (selected === "all" || available.includes(selected)) return selected;
  return available[0];
}

export function eventStoryUpdateAffectsLocale(
  viewLocale: string,
  payloadLocale: string,
  action: string,
): boolean {
  if (action === "retry" || action === "reorder") return true;
  return (payloadLocale.trim() || "zh-CN") === viewLocale;
}

export function findEventStoryUpdateTarget<T extends EventStoryEntryIdentity>(
  entries: readonly T[],
  update: EventStoryUpdateIdentity,
): T | undefined {
  if (update.segmentId) {
    return entries.find((entry) => entry.segmentId === update.segmentId || entry.key === update.segmentId);
  }
  if (!update.episodeNo) return undefined;
  return entries.find((entry) => {
    if (eventStoryEpisodeNo(entry) !== update.episodeNo) return false;
    if (update.entryType === "title") return eventStoryEntryType(entry) === "title";
    if (!update.jpKey || eventStoryEntryType(entry) !== "talk") return false;
    return entry.japanese === update.jpKey || entry.key === `${update.episodeNo}|${update.jpKey}`;
  });
}
