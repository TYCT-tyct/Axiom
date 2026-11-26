import { type ReactNode, useEffect, useRef } from "react";
import { board } from "../board-classes";
import { classes } from "../classes";
import { Icon } from "./icon";
import { ListCount } from "./list-count";
import { SegmentedControl } from "./segmented-control";
import s from "./table-toolbar.module.css";

type Option<V extends string> = { value: V; label: string };
type FilterProps<V extends string> = {
  label: string;
  value: V;
  options: readonly Option<V>[];
  onChange: (value: V) => void;
};

// The most options a segmented control shows before the filter becomes a select.
const segmentedMost = 5;

const typing = (target: EventTarget | null) =>
  target instanceof HTMLElement && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName));

// TableToolbar is the row above a table (the admin's adtool): a search box, one filter, and how many rows the table
// lists, as "3 / 10 条" while a filter or the search hides some (given the count of every row, total). `/` jumps to the
// search box when focus is not in a field, and Escape in the box clears it, as the board's keyboard help says. A filter
// of few options is a segmented control, of more a select. children sit at the end, for a button or a switch.
export function TableToolbar<V extends string>({
  label,
  search,
  filter,
  shown,
  total,
  unit,
  children,
}: {
  label: string;
  search: { label: string; value: string; onChange: (value: string) => void };
  filter?: FilterProps<V>;
  shown: number;
  total: number;
  unit: string;
  children?: ReactNode;
}) {
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    const jump = (event: KeyboardEvent) => {
      if (event.key !== "/" || event.metaKey || event.ctrlKey || event.altKey || typing(event.target)) return;
      if (document.querySelector("dialog[open]")) return;
      event.preventDefault();
      input.current?.focus();
    };
    document.addEventListener("keydown", jump);
    return () => document.removeEventListener("keydown", jump);
  }, []);
  return (
    <search aria-label={label} className={s.root}>
      <label className={s.search}>
        <Icon name="search" />
        <input
          ref={input}
          type="search"
          value={search.value}
          placeholder={search.label}
          aria-label={search.label}
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => search.onChange(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Escape" && search.value !== "") {
              event.preventDefault();
              event.stopPropagation();
              search.onChange("");
            }
          }}
        />
        <kbd>{"/"}</kbd>
      </label>
      {filter ? <Filter {...filter} /> : null}
      <ListCount shown={shown} total={total} unit={unit} />
      {children}
    </search>
  );
}

function Filter<V extends string>({ label, value, options, onChange }: FilterProps<V>) {
  if (options.length <= segmentedMost) {
    return <SegmentedControl label={label} value={value} options={options} onChange={onChange} />;
  }
  return (
    <select
      className={classes(board.qin, s.select)}
      aria-label={label}
      value={value}
      onChange={(event) => onChange(event.target.value as V)}
    >
      {options.map((option) => (
        <option key={option.value} value={option.value}>
          {option.label}
        </option>
      ))}
    </select>
  );
}
