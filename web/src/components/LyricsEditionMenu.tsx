"use client";

import { useCallback, useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { TranslationEditionSummary } from "@/lib/api";

export type LyricsEditionCommand = "create" | "clone" | "rename" | "set-default";

interface LyricsEditionMenuProps {
  editions: TranslationEditionSummary[];
  activeEditionKey: string;
  defaultEditionKey: string;
  disabled?: boolean;
  onSelect: (editionKey: string) => void;
  onCommand: (command: LyricsEditionCommand) => void;
}

interface MenuPosition {
  top: number;
  left: number;
  width: number;
  maxHeight: number;
}

interface ViewportBounds {
  top: number;
  left: number;
  width: number;
  height: number;
}

const VIEWPORT_MARGIN = 8;
const MENU_GAP = 6;
const MENU_ITEM_HEIGHT = 44;
const MAX_MENU_HEIGHT = 420;
const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not(:disabled)",
  "input:not(:disabled)",
  "select:not(:disabled)",
  "textarea:not(:disabled)",
  '[tabindex]:not([tabindex="-1"])',
].join(",");

function clamp(value: number, minimum: number, maximum: number): number {
  return Math.min(Math.max(value, minimum), maximum);
}

function getViewportBounds(): ViewportBounds {
  const viewport = window.visualViewport;
  return viewport
    ? { top: viewport.offsetTop, left: viewport.offsetLeft, width: viewport.width, height: viewport.height }
    : { top: 0, left: 0, width: document.documentElement.clientWidth, height: document.documentElement.clientHeight };
}

export function LyricsEditionMenu({
  editions,
  activeEditionKey,
  defaultEditionKey,
  disabled = false,
  onSelect,
  onCommand,
}: LyricsEditionMenuProps) {
  const [open, setOpen] = useState(false);
  const [mounted, setMounted] = useState(false);
  const [position, setPosition] = useState<MenuPosition>({ top: 0, left: 0, width: 280, maxHeight: 360 });
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const menuId = useId();
  const activeEdition = editions.find((edition) => edition.key === activeEditionKey) || editions[0];

  const closeMenu = useCallback((restoreFocus = false) => {
    setOpen(false);
    if (restoreFocus) window.requestAnimationFrame(() => triggerRef.current?.focus());
  }, []);

  const updatePosition = useCallback(() => {
    const trigger = triggerRef.current;
    if (!trigger) return;
    const rect = trigger.getBoundingClientRect();
    const viewport = getViewportBounds();
    const viewportRight = viewport.left + viewport.width;
    const viewportBottom = viewport.top + viewport.height;
    const maximumWidth = Math.max(0, viewport.width - VIEWPORT_MARGIN * 2);
    const width = Math.min(Math.max(rect.width, 280), maximumWidth);
    const desiredHeight = Math.min(MAX_MENU_HEIGHT, (editions.length + 4) * MENU_ITEM_HEIGHT + 25);
    const roomBelow = Math.max(0, viewportBottom - rect.bottom - MENU_GAP - VIEWPORT_MARGIN);
    const roomAbove = Math.max(0, rect.top - viewport.top - MENU_GAP - VIEWPORT_MARGIN);
    const placeAbove = roomBelow < desiredHeight && roomAbove > roomBelow;
    const sideRoom = placeAbove ? roomAbove : roomBelow;
    const usableViewportHeight = Math.max(0, viewport.height - VIEWPORT_MARGIN * 2);
    const overlapsTrigger = sideRoom < MENU_ITEM_HEIGHT;
    const maxHeight = Math.min(desiredHeight, overlapsTrigger ? usableViewportHeight : sideRoom);
    const minimumTop = viewport.top + VIEWPORT_MARGIN;
    const maximumTop = Math.max(minimumTop, viewportBottom - VIEWPORT_MARGIN - maxHeight);
    const proposedTop = overlapsTrigger
      ? minimumTop
      : placeAbove ? rect.top - MENU_GAP - maxHeight : rect.bottom + MENU_GAP;
    const top = clamp(proposedTop, minimumTop, maximumTop);
    const left = clamp(
      rect.left,
      viewport.left + VIEWPORT_MARGIN,
      Math.max(viewport.left + VIEWPORT_MARGIN, viewportRight - width - VIEWPORT_MARGIN),
    );
    setPosition({ top, left, width, maxHeight });
  }, [editions.length]);

  const focusMenuItem = useCallback((which: "active" | "first" | "last") => {
    const items = Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>(
      '[role="menuitemradio"]:not(:disabled), [role="menuitem"]:not(:disabled)',
    ) || []);
    if (items.length === 0) return;
    if (which === "last") items.at(-1)?.focus();
    else if (which === "active") (items.find((item) => item.dataset.editionKey === activeEditionKey) || items[0]).focus();
    else items[0].focus();
  }, [activeEditionKey]);

  const focusAdjacentControl = useCallback((direction: -1 | 1) => {
    const trigger = triggerRef.current;
    if (!trigger) return;
    const controls = Array.from(document.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter((element) =>
      !menuRef.current?.contains(element)
      && element.getAttribute("aria-disabled") !== "true"
      && element.getAttribute("aria-hidden") !== "true",
    );
    const triggerIndex = controls.indexOf(trigger);
    const target = triggerIndex >= 0 ? controls[triggerIndex + direction] : null;
    (target || trigger).focus();
  }, []);

  const openMenu = useCallback((focus: "active" | "first" | "last" = "active") => {
    if (disabled || editions.length === 0) return;
    updatePosition();
    setOpen(true);
    window.requestAnimationFrame(() => focusMenuItem(focus));
  }, [disabled, editions.length, focusMenuItem, updatePosition]);

  useEffect(() => setMounted(true), []);
  useEffect(() => {
    if (disabled && open) closeMenu(false);
  }, [closeMenu, disabled, open]);

  useEffect(() => {
    if (!open) return;
    updatePosition();
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (!menuRef.current?.contains(target) && !triggerRef.current?.contains(target)) closeMenu(false);
    };
    const onViewportChange = () => closeMenu(true);
    const onViewportScroll = (event: Event) => {
      if (event.target instanceof Node && menuRef.current?.contains(event.target)) return;
      closeMenu(true);
    };
    const visualViewport = window.visualViewport;
    document.addEventListener("pointerdown", onPointerDown, true);
    window.addEventListener("resize", onViewportChange);
    window.addEventListener("scroll", onViewportScroll, true);
    visualViewport?.addEventListener("resize", onViewportChange);
    visualViewport?.addEventListener("scroll", onViewportChange);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown, true);
      window.removeEventListener("resize", onViewportChange);
      window.removeEventListener("scroll", onViewportScroll, true);
      visualViewport?.removeEventListener("resize", onViewportChange);
      visualViewport?.removeEventListener("scroll", onViewportChange);
    };
  }, [closeMenu, open, updatePosition]);

  const handleTriggerKeyDown = (event: React.KeyboardEvent<HTMLButtonElement>) => {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      openMenu("first");
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      openMenu("last");
    } else if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      openMenu("active");
    } else if (event.key === "Escape" && open) {
      event.preventDefault();
      closeMenu(true);
    }
  };

  const handleMenuKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const items = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>(
      '[role="menuitemradio"]:not(:disabled), [role="menuitem"]:not(:disabled)',
    ));
    const currentIndex = items.findIndex((item) => item === document.activeElement);
    let nextIndex = currentIndex;
    if (event.key === "ArrowDown") nextIndex = currentIndex < 0 ? 0 : (currentIndex + 1) % items.length;
    else if (event.key === "ArrowUp") nextIndex = currentIndex < 0 ? items.length - 1 : (currentIndex - 1 + items.length) % items.length;
    else if (event.key === "Home") nextIndex = 0;
    else if (event.key === "End") nextIndex = items.length - 1;
    else if (event.key === "Escape") {
      event.preventDefault();
      closeMenu(true);
      return;
    } else if (event.key === "Tab") {
      event.preventDefault();
      closeMenu(false);
      window.requestAnimationFrame(() => focusAdjacentControl(event.shiftKey ? -1 : 1));
      return;
    } else if ((event.key === "Enter" || event.key === " ") && document.activeElement instanceof HTMLButtonElement) {
      event.preventDefault();
      document.activeElement.click();
      return;
    } else {
      return;
    }
    event.preventDefault();
    items[nextIndex]?.focus();
  };

  const chooseEdition = (editionKey: string) => {
    triggerRef.current?.focus();
    closeMenu(false);
    if (editionKey !== activeEditionKey) onSelect(editionKey);
  };

  const chooseCommand = (command: LyricsEditionCommand) => {
    triggerRef.current?.focus();
    closeMenu(false);
    onCommand(command);
  };

  const menu = open && mounted ? createPortal(
    <div
      ref={menuRef}
      id={menuId}
      className="lyrics-edition-menu"
      role="menu"
      aria-label="选择或管理歌词译本"
      onKeyDown={handleMenuKeyDown}
      style={{ top: position.top, left: position.left, width: position.width, maxHeight: position.maxHeight }}
    >
      <div className="lyrics-edition-menu-list">
        {editions.map((edition) => (
          <button
            type="button"
            role="menuitemradio"
            aria-checked={edition.key === activeEditionKey}
            key={edition.key}
            data-edition-key={edition.key}
            className="lyrics-edition-menu-item"
            onClick={() => chooseEdition(edition.key)}
          >
            <span className="lyrics-edition-check" aria-hidden="true">{edition.key === activeEditionKey ? "✓" : ""}</span>
            <span className="lyrics-edition-menu-copy"><strong>{edition.label}</strong><code>{edition.key}</code></span>
            {edition.key === defaultEditionKey && <span className="lyrics-edition-default-text">默认译本</span>}
          </button>
        ))}
      </div>
      <div className="lyrics-edition-menu-separator" role="separator" />
      <div className="lyrics-edition-menu-commands">
        <button type="button" role="menuitem" disabled={editions.length >= 16} onClick={() => chooseCommand("create")}>新建空白译本</button>
        <button type="button" role="menuitem" disabled={editions.length >= 16} onClick={() => chooseCommand("clone")}>克隆当前译本</button>
        <button type="button" role="menuitem" onClick={() => chooseCommand("rename")}>重命名当前译本</button>
        <button type="button" role="menuitem" disabled={activeEditionKey === defaultEditionKey} onClick={() => chooseCommand("set-default")}>设为默认译本</button>
      </div>
    </div>,
    document.body,
  ) : null;

  return (
    <div className="lyrics-edition-selector">
      <button
        ref={triggerRef}
        type="button"
        className="lyrics-edition-trigger"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        aria-disabled={disabled || editions.length === 0}
        onClick={() => {
          if (disabled || editions.length === 0) return;
          if (open) closeMenu(false);
          else openMenu("active");
        }}
        onKeyDown={handleTriggerKeyDown}
      >
        <span className="lyrics-edition-trigger-copy">
          <strong>{activeEdition?.label || "选择译本"}</strong>
          <code>{activeEdition?.key || "—"}</code>
        </span>
        {activeEdition?.key === defaultEditionKey && <span className="lyrics-edition-default-text">默认译本</span>}
        <span className="lyrics-edition-chevron" aria-hidden="true">⌄</span>
      </button>
      {menu}
    </div>
  );
}
