import type { ReactNode } from "react";
import { board } from "../board-classes";
import { classes } from "../classes";
import s from "./table.module.css";

// A table scrolls sideways inside its own box (.qtw/.qtbl from AxApp/regions.css) when the window is too narrow, and
// never widens the page. The box is a named region in the tab order, as keyboard users can only scroll what takes focus.
// `fixed` lets a table whose columns are all known give each a width and truncate long values inside it; without it,
// columns size from their widest content.
export function Table({
  children,
  label,
  fixed = false,
  widths,
  openable = false,
}: {
  children: ReactNode;
  label: string;
  fixed?: boolean;
  widths?: readonly (number | undefined)[];
  openable?: boolean;
}) {
  return (
    // biome-ignore lint/a11y/noNoninteractiveTabindex: Keyboard users need focus to scroll a table region.
    <section className={classes(board.qtw, s.box)} aria-label={label} tabIndex={0}>
      <table aria-label={label} className={classes(board.qtbl, s.table, fixed && s.fixed, openable && board.hov)}>
        {fixed && widths ? (
          <colgroup>
            {widths.map((width, i) => (
              <col
                key={i}
                className={s.col}
                style={width ? ({ "--w": `${width}px` } as Record<`--${string}`, string>) : undefined}
              />
            ))}
          </colgroup>
        ) : null}
        {children}
      </table>
    </section>
  );
}

type Column = {
  align?: "end";
  shrink?: boolean;
  main?: boolean;
  actions?: boolean;
  from?: "sm" | "md" | "lg" | "xl" | "2xl";
};

const column = ({ align, shrink, main, actions, from }: Column) =>
  classes(
    align === "end" && board.r,
    align === "end" && s.end,
    (shrink || actions) && s.shrink,
    main && s.main,
    actions && s.actions,
    from && s[`from${from}`],
  );

// Tr is a body row. flash lights it once after an edit changed it; onFlashEnd says the light has gone, so the caller
// can flash it again later. A row with onOpen opens what it lists (the admin's data-row): a press on it or Enter while
// it has focus, except a press on a control inside it, which does its own thing. selected marks the row whose drawer is
// open.
export function Tr({
  children,
  flash = false,
  onFlashEnd,
  onOpen,
  selected = false,
}: {
  children: ReactNode;
  flash?: boolean;
  onFlashEnd?: () => void;
  onOpen?: () => void;
  selected?: boolean;
}) {
  return (
    <tr
      className={classes(flash && s.flash, selected && board.on)}
      onAnimationEnd={flash ? onFlashEnd : undefined}
      tabIndex={onOpen ? 0 : undefined}
      aria-selected={onOpen ? selected : undefined}
      onClick={onOpen && ((event) => !onControl(event.target) && onOpen())}
      onKeyDown={
        onOpen &&
        ((event) => {
          if (event.key === "Enter" && event.target === event.currentTarget) onOpen();
        })
      }
    >
      {children}
    </tr>
  );
}

const onControl = (target: EventTarget) =>
  target instanceof Element && target.closest("a, button, input, select, label");

// A sortable header (the admin's adsort) is a button in the cell, and the cell says the order: the arrow points down
// while the column is off or high to low, up when low to high, and turns to the accent once the column sorts.
export type SortOrder = "ascending" | "descending" | "none";

export function Th({
  children,
  sort,
  onSort,
  ...layout
}: { children?: ReactNode; sort?: SortOrder; onSort?: () => void } & Column) {
  return (
    <th scope="col" aria-sort={onSort ? (sort ?? "none") : undefined} className={column(layout)}>
      {onSort ? (
        <button
          type="button"
          className={classes(s.sort, sort !== undefined && sort !== "none" && s.sorted)}
          onClick={onSort}
        >
          <span>{children}</span>
          <svg viewBox="0 0 12 12" aria-hidden="true">
            <path d={sort === "ascending" ? "M6 9.5v-7M3 5.5l3-3 3 3" : "M6 2.5v7M3 6.5l3 3 3-3"} />
          </svg>
        </button>
      ) : (
        children
      )}
    </th>
  );
}

export function Td({
  children,
  colSpan,
  muted = false,
  mono = false,
  ...layout
}: { children?: ReactNode; colSpan?: number; muted?: boolean; mono?: boolean } & Column) {
  return (
    <td colSpan={colSpan} className={classes(column(layout), muted && board.mut, mono && board.mono, mono && s.mono)}>
      {children}
    </td>
  );
}
