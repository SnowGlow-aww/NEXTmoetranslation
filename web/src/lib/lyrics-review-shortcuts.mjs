export function isLyricsReviewInteractiveTarget(target) {
  if (!(target instanceof Element)) return false;
  return Boolean(target.closest("button, a, input, select, textarea, summary, [contenteditable]:not([contenteditable='false']), [role='button'], [role='link'], [role='checkbox'], [role='radio'], [role='switch'], [role='menuitem'], [role='menuitemcheckbox'], [role='menuitemradio'], [role='option'], [role='tab'], [role='textbox'], [role='searchbox'], [role='combobox'], [role='listbox'], [role='slider'], [role='spinbutton'], [role='treeitem'], [role='gridcell']"));
}

export function isLyricsReviewEditableTarget(target) {
  if (!(target instanceof Element)) return false;
  return Boolean(target.closest("input, select, textarea, [contenteditable]"));
}

export function lyricsReviewShortcutAction(event, context) {
  if (event.defaultPrevented || event.repeat || event.isComposing) return null;

  const key = event.key.toLowerCase();
  if (context.modalOpen) {
    if (event.key === "Escape" && !event.metaKey && !event.ctrlKey && !event.altKey && !event.shiftKey) {
      return context.submitting ? null : "close-modal";
    }
    if (context.busy || !context.confirmEligible || isLyricsReviewInteractiveTarget(event.target)) return null;
    if (event.key === "Enter" && !event.metaKey && !event.ctrlKey && !event.altKey && !event.shiftKey) return "confirm";
    return null;
  }
  if (context.busy) return null;
  if (isLyricsReviewInteractiveTarget(event.target)) return null;

  if (event.key === "Escape") return "clear-selection";
  if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey) {
    if (event.key === "ArrowUp") return "previous";
    if (event.key === "ArrowDown") return "next";
    if (key === "a") return "toggle-all";
  }
  if (!event.metaKey && !event.ctrlKey && !event.altKey && event.shiftKey) {
    if (key === "a") return "approve";
    if (key === "r") return "reject";
  }
  if (!event.metaKey && !event.ctrlKey && !event.altKey && !event.shiftKey && (event.code === "Space" || event.key === " ")) {
    return "toggle-active";
  }
  return null;
}
