export interface LyricsUpdateEvent {
  musicId: number;
  revision: number;
  clientId: string;
  editionKey?: string;
  renditionKey?: string;
  side?: "full" | "game" | "credits";
  locale?: "zh-CN";
}

export interface LyricsEditorTarget {
  musicId: number;
  editionKey: string;
  renditionKey: string;
  side: "full" | "game";
  locale: "zh-CN";
  projectionKind?: "full_only" | "game_only" | "exact_projection" | "independent_game" | "invalid";
}

export function normalizeLyricsUpdateEvent(value: unknown): LyricsUpdateEvent | null;
export function lyricsUpdateMatchesEditorTarget(update: LyricsUpdateEvent, target: LyricsEditorTarget): boolean;
export function lyricsUpdateTargetLabel(update: LyricsUpdateEvent): string;
