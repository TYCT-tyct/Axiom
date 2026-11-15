import {
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import { useTranslation } from "react-i18next";
import { classes } from "../classes";
import { ActionButton } from "./action-button";
import s from "./list-detail.module.css";

// The width at which the detail stops fitting beside the list and covers it (tablet and phone).
const overlayQuery = "(max-width: 1023px)";
// Rows are drawn in chunks: the window moves only when it crosses a chunk, so a scroll re-renders every few dozen rows
// rather than every frame.
const chunk = 10;
const overscan = 10;

function useOverlay() {
  return useSyncExternalStore(
    (notify) => {
      const query = window.matchMedia(overlayQuery);
      query.addEventListener("change", notify);
      return () => query.removeEventListener("change", notify);
    },
    () => window.matchMedia(overlayQuery).matches,
    () => false,
  );
}

// useRowWindow tells which rows of a list the viewport shows. The page scrolls, not the list, so it reads where the
// list's top is in the viewport instead of the scroll position of a box.
function useRowWindow(list: RefObject<HTMLElement | null>, count: number, rowHeight: number) {
  const [range, setRange] = useState({ start: 0, end: 0 });
  const measure = useCallback(() => {
    const top = list.current?.getBoundingClientRect().top ?? 0;
    const first = Math.floor(-top / rowHeight) - overscan;
    const last = Math.ceil((window.innerHeight - top) / rowHeight) + overscan;
    const start = Math.max(0, Math.floor(first / chunk) * chunk);
    const end = Math.min(count, Math.ceil(last / chunk) * chunk);
    setRange((was) => (was.start === start && was.end === end ? was : { start, end }));
  }, [list, count, rowHeight]);
  useLayoutEffect(measure, [measure]);
  useEffect(() => {
    window.addEventListener("scroll", measure, { passive: true });
    window.addEventListener("resize", measure);
    return () => {
      window.removeEventListener("scroll", measure);
      window.removeEventListener("resize", measure);
    };
  }, [measure]);
  return range;
}

// The detail: a region beside the list, or on a tablet and phone a modal panel over it with a button, Esc and the scrim to
// close it. A panel takes focus as it opens; the list gives it back to the row as it closes.
function DetailPane({
  label,
  overlay,
  onClose,
  children,
}: {
  label: string;
  overlay: boolean;
  onClose: () => void;
  children: ReactNode;
}) {
  const { t } = useTranslation("common");
  const pane = useRef<HTMLElement>(null);
  useEffect(() => {
    if (overlay) {
      pane.current?.focus();
    }
  }, [overlay]);
  const kind = overlay ? ({ role: "dialog", "aria-modal": true } as const) : ({ role: "region" } as const);
  return (
    <>
      {overlay ? <div className={s.scrim} onClick={onClose} aria-hidden="true" /> : null}
      <aside
        ref={pane}
        tabIndex={-1}
        {...kind}
        aria-label={label}
        className={classes(s.detail, overlay && s.panel)}
        onKeyDown={(event) => {
          if (overlay && event.key === "Escape") {
            event.stopPropagation();
            onClose();
          }
        }}
      >
        {overlay ? (
          <div className={s.bar}>
            <ActionButton variant="gho" size="sm" icon="close" aria-label={t("dialog.close")} onClick={onClose} />
          </div>
        ) : null}
        {children}
      </aside>
    </>
  );
}

// Where a key moves the focus among rows 0…last from the row at `from`; undefined for a key that does not move it.
function moveKey(key: string, from: number, last: number) {
  const to: Record<string, number> = {
    ArrowDown: Math.min(from + 1, last),
    ArrowUp: Math.max(from - 1, 0),
    Home: 0,
    End: last,
  };
  return to[key];
}

// How a selection came about, so the caller can write it to the address the right way (useUrlSelection).
type SelectBy = "pointer" | "key" | "close";

// Scrolls the page just enough that the row at `at` is in the viewport.
function reveal(list: HTMLElement | null, at: number, rowHeight: number) {
  const top = (list?.getBoundingClientRect().top ?? 0) + at * rowHeight;
  if (top < 0) {
    window.scrollBy(0, top);
  } else if (top + rowHeight > window.innerHeight) {
    window.scrollBy(0, top + rowHeight - window.innerHeight);
  }
}

type Select = (key: string | null, by: SelectBy) => void;

// useRoving keeps the list's one tab stop: the row last focused or selected, else the first, and moves it with the keys.
// The stop row is always drawn (see Rows), so focus never lands on a row the window dropped.
function useRoving<Row>({
  rows,
  rowKey,
  indexOf,
  selected,
  rowHeight,
  overlay,
  list,
  onSelect,
}: {
  rows: readonly Row[];
  rowKey: (row: Row) => string;
  indexOf: ReadonlyMap<string, number>;
  selected: string | null;
  rowHeight: number;
  overlay: boolean;
  list: RefObject<HTMLElement | null>;
  onSelect: Select;
}) {
  const [stopKey, setStopKey] = useState<string | null>(selected);
  // A selection that arrives from outside (Back, a pasted link) moves the stop to it.
  const [follow, setFollow] = useState<string | null>(selected);
  if (selected !== follow) {
    setFollow(selected);
    if (selected !== null) {
      setStopKey(selected);
    }
  }
  const stopAt = stopKey === null ? 0 : (indexOf.get(stopKey) ?? 0);
  const focusRow = useRef(false);
  useLayoutEffect(() => {
    if (focusRow.current) {
      focusRow.current = false;
      list.current?.querySelector<HTMLElement>(`[data-at="${stopAt}"]`)?.focus({ preventScroll: true });
    }
  });
  const moveTo = (at: number) => {
    const target = rows[at];
    if (target !== undefined) {
      const key = rowKey(target);
      reveal(list.current, at, rowHeight);
      setStopKey(key);
      focusRow.current = true;
      if (!overlay) {
        onSelect(key, "key");
      }
    }
  };
  const onKeyDown = (event: KeyboardEvent<HTMLElement>) => {
    const focused = Number((event.target as HTMLElement).dataset.at ?? stopAt);
    const to = moveKey(event.key, focused, rows.length - 1);
    const current = rows[focused];
    if (to !== undefined) {
      event.preventDefault();
      moveTo(to);
    } else if ((event.key === "Enter" || event.key === " ") && current !== undefined) {
      event.preventDefault();
      onSelect(rowKey(current), "pointer");
    }
  };
  // Closing the panel puts focus back on the row it came from, which is the stop.
  const close = () => {
    focusRow.current = true;
    onSelect(null, "close");
  };
  return { stopAt, setStopKey, onKeyDown, close };
}

// The listbox: the rows near the viewport, each placed by its index, and the stop row wherever it is.
function Rows<Row>({
  rows,
  rowKey,
  renderRow,
  selected,
  onSelect,
  range,
  stopAt,
  setStopKey,
}: {
  rows: readonly Row[];
  rowKey: (row: Row) => string;
  renderRow: (row: Row) => ReactNode;
  selected: string | null;
  onSelect: Select;
  range: { start: number; end: number };
  stopAt: number;
  setStopKey: (key: string) => void;
}) {
  const drawn = useMemo(() => {
    const at = Array.from({ length: Math.max(0, range.end - range.start) }, (_, n) => range.start + n);
    if (rows.length > 0 && (stopAt < range.start || stopAt >= range.end)) {
      at.push(stopAt);
      at.sort((a, b) => a - b);
    }
    return at;
  }, [range, stopAt, rows.length]);
  return drawn.map((at) => {
    const item = rows[at] as Row;
    const key = rowKey(item);
    return (
      // biome-ignore lint/a11y/useKeyWithClickEvents: the listbox handles the keys for every row.
      <div
        key={key}
        role="option"
        tabIndex={at === stopAt ? 0 : -1}
        aria-selected={key === selected}
        aria-setsize={rows.length}
        aria-posinset={at + 1}
        data-at={at}
        className={classes(s.row, key === selected && s.on)}
        style={{ "--at": at } as Record<`--${string}`, number>}
        onClick={() => {
          setStopKey(key);
          onSelect(key, "pointer");
        }}
        onFocus={() => setStopKey(key)}
      >
        {renderRow(item)}
      </div>
    );
  });
}

// ListDetail is a long list with the detail of one row beside it. The list draws only the rows near the viewport, at one
// fixed height (rowHeight), and the page scrolls it. It is a listbox: Up, Down, Home and End move among the rows, and
// on a wide window the selection follows, so the detail follows too; on a tablet or phone the detail is a panel over the
// list, so the keys only move focus and Enter or a tap opens the panel. Closing the panel (its button, Esc, a tap
// outside) puts focus back on the row it came from. The selection is the caller's, normally in the address
// (useUrlSelection); a key that is no longer in rows reads as no selection. A row's content is read as one option, so
// it holds text, not controls.
export function ListDetail<Row>({
  label,
  detailLabel,
  placeholder,
  rows,
  rowKey,
  rowHeight = 44,
  renderRow,
  selected,
  onSelect,
  detail,
}: {
  label: string;
  detailLabel: string;
  // What the detail pane says when no row is selected.
  placeholder: string;
  rows: readonly Row[];
  rowKey: (row: Row) => string;
  rowHeight?: number;
  renderRow: (row: Row) => ReactNode;
  selected: string | null;
  onSelect: Select;
  detail: (row: Row) => ReactNode;
}) {
  const overlay = useOverlay();
  const list = useRef<HTMLDivElement>(null);
  const indexOf = useMemo(() => new Map(rows.map((row, at) => [rowKey(row), at])), [rows, rowKey]);
  const row = selected === null ? undefined : rows[indexOf.get(selected) ?? -1];
  const range = useRowWindow(list, rows.length, rowHeight);
  const { stopAt, setStopKey, onKeyDown, close } = useRoving({
    rows,
    rowKey,
    indexOf,
    selected,
    rowHeight,
    overlay,
    list,
    onSelect,
  });
  return (
    <div className={s.root}>
      <div
        ref={list}
        role="listbox"
        aria-label={label}
        className={s.list}
        style={{ "--rows": rows.length, "--row-h": `${rowHeight}px` } as Record<`--${string}`, string | number>}
        inert={overlay && row !== undefined}
        onKeyDown={onKeyDown}
      >
        <Rows
          rows={rows}
          rowKey={rowKey}
          renderRow={renderRow}
          selected={selected}
          onSelect={onSelect}
          range={range}
          stopAt={stopAt}
          setStopKey={setStopKey}
        />
      </div>
      {!overlay || row !== undefined ? (
        <DetailPane label={detailLabel} overlay={overlay} onClose={close}>
          {row === undefined ? <p className={s.placeholder}>{placeholder}</p> : detail(row)}
        </DetailPane>
      ) : null}
    </div>
  );
}
