import { type KeyboardEvent, type RefObject, useEffect, useId, useMemo, useRef, useState } from "react";
import s from "./command-palette.module.css";
import { Glyph, type GlyphName } from "./glyph";
import { moves } from "./menu";

// One thing the palette can open or do. meta says what kind of thing it is (页面, 设置, 操作) or where it is; keywords
// are more words it is found by.
export type PaletteItem = {
  key: string;
  label: string;
  meta?: string;
  icon?: GlyphName;
  keywords?: string;
  onSelect: () => void;
};

// A source of items. With an empty query the palette lists the sources marked idle, in order, under their labels (the
// board's 最近看过, then the common actions, then the pages); a query searches every source.
export type PaletteSection = { label: string; items: readonly PaletteItem[]; idle?: boolean };

export type CommandPaletteText = {
  label: string;
  placeholder: string;
  empty: (query: string) => string;
  move: string;
  open: string;
  close: string;
};

// A row of the list: the item, the heading of its source above it when it is the first, and a key unique in the list
// (an item can be in two sources, such as a page that is also among the recent ones).
type Row = PaletteItem & { row: string; heading?: string };

// A query matches an item whose words hold it; the items whose name starts with it come first (adPalItems).
function search(sections: readonly PaletteSection[], query: string): Row[] {
  const q = query.trim().toLowerCase();
  if (!q) {
    return sections
      .filter((section) => section.idle)
      .flatMap((section, n) =>
        section.items.map((item, i) => ({
          ...item,
          row: `${n}:${item.key}`,
          heading: i === 0 ? section.label : undefined,
        })),
      );
  }
  const seen = new Set<string>();
  const hits = sections
    .flatMap((section) => section.items)
    .filter((item) => {
      const words = `${item.label} ${item.meta ?? ""} ${item.keywords ?? ""}`.toLowerCase();
      if (seen.has(item.key) || !words.includes(q)) return false;
      seen.add(item.key);
      return true;
    });
  const starts = (item: PaletteItem) => item.label.toLowerCase().startsWith(q);
  return [...hits.filter(starts), ...hits.filter((item) => !starts(item))]
    .slice(0, 24)
    .map((item) => ({ ...item, row: item.key }));
}

// The key caps the palette draws; they name keys, not words, so no language changes them.
const caps = { up: "↑", down: "↓", enter: "↵", esc: "esc" };

// useHotkey opens and closes the palette with ⌘K or Ctrl K from anywhere on the page.
function useHotkey(open: boolean, onOpenChange: (open: boolean) => void) {
  useEffect(() => {
    const press = (event: globalThis.KeyboardEvent) => {
      if (event.key.toLowerCase() !== "k" || !(event.metaKey || event.ctrlKey) || event.altKey || event.shiftKey)
        return;
      event.preventDefault();
      onOpenChange(!open);
    };
    window.addEventListener("keydown", press);
    return () => window.removeEventListener("keydown", press);
  }, [open, onOpenChange]);
}

// CommandPalette is the board's ⌘K search (adPal* in admin_core.js) as a native modal dialog: focus stays in the
// query, the arrow keys, Home and End move the active option, Enter opens it, Escape or a press outside closes the
// palette and gives focus back. The app gives it the items by source, so a later source joins without a change here.
export function CommandPalette({
  open,
  onOpenChange,
  sections,
  text,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  sections: readonly PaletteSection[];
  text: CommandPaletteText;
}) {
  useHotkey(open, onOpenChange);
  return open ? <Palette sections={sections} text={text} onClose={() => onOpenChange(false)} /> : null;
}

// The query field's keys: the arrows, Home and End move the active option, Enter opens it (not while an input method
// is still composing the text).
function listKeys(rows: Row[], active: number, setAt: (at: number) => void, choose: (row: Row | undefined) => void) {
  return (event: KeyboardEvent<HTMLInputElement>) => {
    const step = moves[event.key];
    if (step && rows.length) {
      event.preventDefault();
      setAt(step(active, rows.length));
    } else if (event.key === "Enter" && !event.nativeEvent.isComposing) {
      event.preventDefault();
      choose(rows[active]);
    }
  };
}

function Palette({
  sections,
  text,
  onClose,
}: {
  sections: readonly PaletteSection[];
  text: CommandPaletteText;
  onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const id = useId();
  const [query, setQuery] = useState("");
  const [at, setAt] = useState(0);
  const rows = useMemo(() => search(sections, query), [sections, query]);
  const active = Math.min(at, rows.length - 1);
  useEffect(() => {
    if (dialog.current && !dialog.current.open) dialog.current.showModal();
  }, []);
  useEffect(() => {
    list.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: "nearest" });
  });
  const choose = (row: Row | undefined) => {
    if (!row) return;
    // Closing the dialog gives focus back to where it was, before the item moves it on.
    dialog.current?.close();
    row.onSelect();
  };
  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: a press on the backdrop closes; the keyboard's way is Escape.
    <dialog
      ref={dialog}
      aria-label={text.label}
      className={s.root}
      onClose={onClose}
      onClick={(event) => {
        if (event.target === dialog.current) dialog.current.close();
      }}
    >
      <div className={s.panel}>
        <QueryField
          id={id}
          query={query}
          active={rows.length ? active : undefined}
          text={text}
          onQuery={(next) => {
            setQuery(next);
            setAt(0);
          }}
          onKeyDown={listKeys(rows, active, setAt, choose)}
        />
        <Options
          id={id}
          rows={rows}
          active={active}
          query={query}
          text={text}
          list={list}
          hover={setAt}
          choose={choose}
        />
        <Keys text={text} />
      </div>
    </dialog>
  );
}

// The query, a combobox over the list: focus stays here and the active option is the one it points at.
function QueryField({
  id,
  query,
  active,
  text,
  onQuery,
  onKeyDown,
}: {
  id: string;
  query: string;
  active: number | undefined;
  text: CommandPaletteText;
  onQuery: (query: string) => void;
  onKeyDown: (event: KeyboardEvent<HTMLInputElement>) => void;
}) {
  return (
    <label className={s.field}>
      <Glyph name="search" size={20} />
      <input
        className={s.input}
        value={query}
        placeholder={text.placeholder}
        aria-label={text.label}
        role="combobox"
        aria-expanded="true"
        aria-controls={`${id}-list`}
        aria-activedescendant={active === undefined ? undefined : `${id}-${active}`}
        autoComplete="off"
        spellCheck={false}
        onChange={(event) => onQuery(event.target.value)}
        onKeyDown={onKeyDown}
      />
      <kbd className={s.key}>{caps.esc}</kbd>
    </label>
  );
}

function Keys({ text }: { text: CommandPaletteText }) {
  return (
    <div className={s.foot} aria-hidden="true">
      <span>
        <kbd className={s.key}>{caps.up}</kbd>
        <kbd className={s.key}>{caps.down}</kbd>
        {text.move}
      </span>
      <span>
        <kbd className={s.key}>{caps.enter}</kbd>
        {text.open}
      </span>
      <span>
        <kbd className={s.key}>{caps.esc}</kbd>
        {text.close}
      </span>
    </div>
  );
}

function Options({
  id,
  rows,
  active,
  query,
  text,
  list,
  hover,
  choose,
}: {
  id: string;
  rows: Row[];
  active: number;
  query: string;
  text: CommandPaletteText;
  list: RefObject<HTMLDivElement | null>;
  hover: (at: number) => void;
  choose: (row: Row) => void;
}) {
  if (!rows.length) {
    return <p className={s.empty}>{text.empty(query.trim())}</p>;
  }
  return (
    <div ref={list} id={`${id}-list`} role="listbox" aria-label={text.label} className={s.list}>
      {rows.map((row, i) => [
        row.heading ? (
          <div key={`${row.row}-heading`} role="presentation" className={s.heading}>
            {row.heading}
          </div>
        ) : null,
        // biome-ignore lint/a11y/useKeyWithClickEvents: the keys are the query field's, which keeps focus.
        <div
          key={row.row}
          id={`${id}-${i}`}
          role="option"
          tabIndex={-1}
          aria-selected={i === active}
          className={s.option}
          onPointerMove={() => hover(i)}
          onClick={() => choose(row)}
        >
          {row.icon ? <Glyph name={row.icon} /> : null}
          <span className={s.name}>{row.label}</span>
          {row.meta ? <span className={s.meta}>{row.meta}</span> : null}
          <span className={s.enter} aria-hidden="true">
            {caps.enter}
          </span>
        </div>,
      ])}
    </div>
  );
}
