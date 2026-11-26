import { ActionButton, Field } from "@axiom/ui";
import { type KeyboardEvent, useEffect, useId, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { VendorIcon } from "./icons/vendor-icon";
import { findTypes, modelCount, type SupplierType, typeLine, unsellable } from "./supplier-types";
import s from "./type-picker.module.css";

// The type picker of 添加上游账号: a query over the types, a combobox whose list keeps the groups' order. Focus stays in
// the query; the arrow keys, Home and End move the active option and Enter takes it. A type with nothing to sell yet is
// listed greyed with the reason, and cannot be taken.
export function TypePicker({
  types,
  onPick,
}: {
  types: readonly SupplierType[];
  onPick: (type: SupplierType) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const input = useRef<HTMLInputElement>(null);
  const [query, setQuery] = useState("");
  const sections = useMemo(() => findTypes(types, query), [types, query]);
  const rows = sections.flatMap((section) => section.types);
  const choose = (type: SupplierType | undefined) => {
    if (type && !unsellable(type)) onPick(type);
  };
  const cursor = useCursor(rows, choose);
  useEffect(() => input.current?.focus(), []);
  return (
    <div className={s.root}>
      <Field
        ref={input}
        label={t("upstream.types.search")}
        placeholder={t("upstream.types.placeholder")}
        value={query}
        onChange={(event) => {
          setQuery(event.target.value);
          cursor.reset();
        }}
        onKeyDown={cursor.keys}
        role="combobox"
        aria-expanded="true"
        aria-controls={`${id}-list`}
        aria-activedescendant={rows.length > 0 ? `${id}-${cursor.active}` : undefined}
        autoComplete="off"
        spellCheck={false}
      />
      {rows.length === 0 ? (
        <p className={s.empty} role="status">
          {t("upstream.types.empty", { query: query.trim() })}
        </p>
      ) : (
        <TypeList id={id} sections={sections} rows={rows} cursor={cursor} onChoose={choose} />
      )}
    </div>
  );
}

type Cursor = ReturnType<typeof useCursor>;

// The active option of the list, moved by the query's keys; Enter takes it (not while an input method is composing).
function useCursor(rows: readonly SupplierType[], choose: (type: SupplierType | undefined) => void) {
  const [at, setAt] = useState(0);
  const active = Math.min(at, rows.length - 1);
  const keys = (event: KeyboardEvent<HTMLInputElement>) => {
    const last = rows.length - 1;
    const moves: Record<string, number> = {
      ArrowDown: Math.min(active + 1, last),
      ArrowUp: Math.max(active - 1, 0),
      Home: 0,
      End: last,
    };
    const to = moves[event.key];
    if (to !== undefined && rows.length > 0) {
      event.preventDefault();
      setAt(to);
    } else if (event.key === "Enter" && !event.nativeEvent.isComposing) {
      event.preventDefault();
      choose(rows[active]);
    }
  };
  return { active, keys, moveTo: setAt, reset: () => setAt(0) };
}

// The options under their groups' headings; the active one is kept in view as it moves.
function TypeList({
  id,
  sections,
  rows,
  cursor,
  onChoose,
}: {
  id: string;
  sections: ReturnType<typeof findTypes>;
  rows: readonly SupplierType[];
  cursor: Cursor;
  onChoose: (type: SupplierType) => void;
}) {
  const { t } = useTranslation();
  const list = useRef<HTMLDivElement>(null);
  useEffect(() => {
    list.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: "nearest" });
  });
  return (
    <div ref={list} id={`${id}-list`} role="listbox" aria-label={t("upstream.types.label")} className={s.list}>
      {sections.map((section) => (
        // biome-ignore lint/a11y/useSemanticElements: a listbox's options are grouped by role; a fieldset is a form's.
        <div key={section.group} role="group" aria-labelledby={`${id}-${section.group}`}>
          <div id={`${id}-${section.group}`} className={s.heading}>
            {t(`upstream.types.groups.${section.group}`)}
          </div>
          {section.types.map((type) => {
            const n = rows.indexOf(type);
            return (
              <TypeOption
                key={type.kind}
                id={`${id}-${n}`}
                type={type}
                active={n === cursor.active}
                onHover={() => cursor.moveTo(n)}
                onChoose={() => onChoose(type)}
              />
            );
          })}
        </div>
      ))}
    </div>
  );
}

function TypeOption({
  id,
  type,
  active,
  onHover,
  onChoose,
}: {
  id: string;
  type: SupplierType;
  active: boolean;
  onHover: () => void;
  onChoose: () => void;
}) {
  const { t } = useTranslation();
  const greyed = unsellable(type);
  const count = modelCount(type);
  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: the keys are the query's, which keeps focus.
    <div
      id={id}
      role="option"
      tabIndex={-1}
      aria-selected={active}
      aria-disabled={greyed || undefined}
      className={s.option}
      onPointerMove={onHover}
      onClick={onChoose}
    >
      <TypeLabel type={type} line={greyed ? t("upstream.types.noModels") : typeLine(type, t)} />
      {count === undefined ? null : <span className={s.count}>{t("upstream.types.models", { count })}</span>}
    </div>
  );
}

// A type's mark, name and the line under it, in the list and over the fields it asks for.
function TypeLabel({ type, line }: { type: SupplierType; line: string }) {
  return (
    <>
      <VendorIcon vendor={type.vendor} name={type.name} />
      <span className={s.text}>
        <span className={s.name}>{type.name}</span>
        <span className={s.line}>{line}</span>
      </span>
    </>
  );
}

// The type chosen, over the fields it asks for, with the way back to the picker.
export function ChosenType({ type, onChange }: { type: SupplierType; onChange: () => void }) {
  const { t } = useTranslation();
  return (
    <div className={s.chosen}>
      <TypeLabel type={type} line={typeLine(type, t)} />
      <ActionButton size="sm" variant="gho" onClick={onChange}>
        {t("upstream.types.change")}
      </ActionButton>
    </div>
  );
}
