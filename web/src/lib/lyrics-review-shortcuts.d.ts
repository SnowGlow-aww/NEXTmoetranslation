export type LyricsReviewShortcutAction =
  | "confirm"
  | "close-modal"
  | "clear-selection"
  | "previous"
  | "next"
  | "toggle-all"
  | "approve"
  | "reject"
  | "toggle-active";

export interface LyricsReviewShortcutContext {
  busy: boolean;
  modalOpen: boolean;
  submitting: boolean;
  confirmEligible: boolean;
}

export function isLyricsReviewInteractiveTarget(target: EventTarget | null): boolean;
export function isLyricsReviewEditableTarget(target: EventTarget | null): boolean;
export function lyricsReviewShortcutAction(event: KeyboardEvent, context: LyricsReviewShortcutContext): LyricsReviewShortcutAction | null;
