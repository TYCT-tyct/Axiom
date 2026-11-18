import { useEffect, useId, useRef, useState } from "react";
import DateRangesRegion from "./AxApp/regions/PartDateRanges.tsx";
import FilterBarRegion from "./AxApp/regions/PartFilterBar.tsx";
import FilterClearRegion from "./AxApp/regions/PartFilterClear.tsx";
import FilterOptionRegion from "./AxApp/regions/PartFilterOption.tsx";
import FilterSelectRegion from "./AxApp/regions/PartFilterSelect.tsx";
import ListEndRegion from "./AxApp/regions/PartListEnd.tsx";
import LoadMoreRegion from "./AxApp/regions/PartLoadMore.tsx";
import SearchRegion from "./AxApp/regions/PartSearch.tsx";
import SortableHeaderRegion from "./AxApp/regions/PartSortableHeader.tsx";

type DateRange = { label: string; from: string; to: string };
type Ranges = readonly [DateRange, DateRange, DateRange, DateRange, DateRange];
type Choice = { value: string; label: string; color?: string };
type Select = {
  label: string;
  summary: string;
  allLabel: string;
  options: readonly Choice[];
  selected: readonly string[];
  onChange: (values: string[]) => void;
};

function FilterSelect({ label, summary, allLabel, options, selected, onChange }: Select) {
  const id = useId();
  const select = useRef<Element>(null);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (!open) return;
    const close = (event: Event) => {
      if (!select.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", close);
    document.addEventListener("focusin", close);
    return () => {
      document.removeEventListener("pointerdown", close);
      document.removeEventListener("focusin", close);
    };
  }, [open]);

  return (
    <FilterSelectRegion
      t={String}
      text={{ label: summary }}
      values={{
        select: (element) => {
          select.current = element;
        },
        keyDown: (event) => {
          if (event.key !== "Escape" || !open) return;
          event.stopPropagation();
          setOpen(false);
          select.current?.querySelector("button")?.focus();
        },
        open,
        closed: !open,
        listId: id,
        filterLabel: label,
        openClass: open ? "open" : "",
        activeClass: selected.length ? "on" : "",
        toggle: () => setOpen(!open),
      }}
    >
      {options.map((option) => {
        const pressed = selected.includes(option.value);
        return (
          <FilterOptionRegion
            key={option.value}
            t={String}
            text={{ label: option.label }}
            values={{
              selected: pressed,
              selectedClass: pressed ? "on" : "",
              color: option.color ? `background: ${option.color}` : undefined,
              choose: () =>
                onChange(pressed ? selected.filter((value) => value !== option.value) : [...selected, option.value]),
            }}
          />
        );
      })}
      <FilterClearRegion t={String} text={{ label: allLabel }} values={{ clear: () => onChange([]) }} />
    </FilterSelectRegion>
  );
}

// The five positions of the board's range selector. The caller decides the dates and fetches the chosen range.
export function FilterBar({
  label,
  search,
  selects,
  dates,
}: {
  label: string;
  search: { label: string; placeholder: string; initial?: string; onChange: (value: string) => void };
  selects: readonly Select[];
  dates: { label: string; ranges: Ranges; selected: 0 | 1 | 2 | 3 | 4; onChange: (range: DateRange) => void };
}) {
  const values: Record<string, unknown> = { label: dates.label, selection: `--n: 5; --i: ${dates.selected}` };
  for (const [index, range] of dates.ranges.entries()) {
    const pressed = dates.selected === index;
    values[`active${index}`] = pressed ? "on" : "";
    values[`pressed${index}`] = pressed;
    values[`choose${index}`] = () => dates.onChange(range);
  }
  const [a, b, c, d, e] = dates.ranges;
  return (
    <FilterBarRegion t={String} values={{ label }}>
      <DateRangesRegion
        t={String}
        text={{ range0: a.label, range1: b.label, range2: c.label, range3: d.label, range4: e.label }}
        values={values}
      />
      {selects.map((select) => (
        <FilterSelect key={select.label} {...select} />
      ))}
      <SearchRegion
        t={String}
        text={{ placeholder: search.placeholder }}
        values={{
          label: search.label,
          initial: search.initial,
          input: (event) => search.onChange((event.currentTarget as HTMLInputElement).value),
        }}
      />
    </FilterBarRegion>
  );
}

export function LoadMore(
  props: { label: string } & ({ state: "ready"; onLoadMore: () => void } | { state: "loading" | "end" }),
) {
  if (props.state === "end") return <ListEndRegion t={String} label={props.label} />;
  return (
    <LoadMoreRegion
      t={String}
      text={{ label: props.label }}
      values={{ pending: props.state === "loading", load: props.state === "ready" ? props.onLoadMore : undefined }}
    />
  );
}

type Direction = "ascending" | "descending";

export function SortableHeader({
  children,
  direction,
  onSort,
}: {
  children: string;
  direction?: Direction;
  onSort: (direction: Direction) => void;
}) {
  const sort = () => onSort(direction === "descending" ? "ascending" : "descending");
  return (
    <SortableHeaderRegion
      t={String}
      text={{ label: children }}
      values={{
        selected: direction ? "on" : "",
        sort,
        keyDown: (event) => {
          if (event.key !== "Enter" && event.key !== " ") return;
          event.preventDefault();
          sort();
        },
        header: (header) => {
          if (direction) header?.setAttribute("aria-sort", direction);
          else header?.removeAttribute("aria-sort");
        },
      }}
    />
  );
}
