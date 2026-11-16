import { useTranslation } from "react-i18next";
import { ActionButton } from "./action-button";
import { Field, Select } from "./field";
import s from "./filter-bar.module.css";
import { SegmentedControl } from "./segmented-control";

type Option = { value: string; label: string };
type Common<K extends string> = { key: K; label: string; initial: string };

// One filter of a page: a search box, a choice from a long list, or a choice among a few. initial is the value that
// filters nothing ("" for a search, "all" for a status); the bar is "clear" when every filter holds it.
export type Filter<K extends string> =
  | (Common<K> & { kind: "search"; placeholder?: string })
  | (Common<K> & { kind: "select"; options: readonly Option[] })
  | (Common<K> & { kind: "segmented"; options: readonly Option[] });

// filterDefaults is the values of a bar with nothing filtered: what useUrlParams takes as its defaults, so the filters
// are defined once.
export function filterDefaults<K extends string>(filters: readonly Filter<K>[]) {
  return Object.fromEntries(filters.map((filter) => [filter.key, filter.initial])) as Record<K, string>;
}

// FilterBar is the row of filters above a list. It holds no state: the values are the caller's, normally the address's
// search params (useUrlParams), so a filtered list survives reload and Back. "Clear" shows only while something is
// filtered and resets every filter at once.
export function FilterBar<K extends string>({
  label,
  filters,
  values,
  onChange,
  onClear,
}: {
  label: string;
  filters: readonly Filter<K>[];
  values: Record<K, string>;
  onChange: (key: K, value: string) => void;
  onClear: () => void;
}) {
  const { t } = useTranslation("common");
  const active = filters.some((filter) => values[filter.key] !== filter.initial);
  return (
    <search aria-label={label} className={s.root}>
      {filters.map((filter) => (
        <div key={filter.key} className={filter.kind === "search" ? s.search : s.item}>
          {filter.kind === "search" ? (
            <Field
              label={filter.label}
              type="search"
              placeholder={filter.placeholder}
              value={values[filter.key]}
              onChange={(event) => onChange(filter.key, event.target.value)}
            />
          ) : filter.kind === "select" ? (
            <Select
              label={filter.label}
              options={filter.options}
              value={values[filter.key]}
              onChange={(event) => onChange(filter.key, event.target.value)}
            />
          ) : (
            <SegmentedControl
              label={filter.label}
              options={filter.options}
              value={values[filter.key]}
              onChange={(value) => onChange(filter.key, value)}
            />
          )}
        </div>
      ))}
      {active ? (
        <div className={s.clear}>
          <ActionButton variant="gho" icon="close" onClick={onClear}>
            {t("filters.clear")}
          </ActionButton>
        </div>
      ) : null}
    </search>
  );
}
