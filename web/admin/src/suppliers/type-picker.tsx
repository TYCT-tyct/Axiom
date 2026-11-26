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
  const list = useRef<HTMLDivElement>(null);
  const [query, setQuery] = useState("");
  const [at, setAt] = useState(0);
  const sections = useMemo(() => findTypes(types, query), [types, query]);
  const rows = sections.flatMap((section) => section.types);
  const active = Math.min(at, rows.length - 1);
  useEffect(() => input.current?.focus(), []);
  useEffect(() => {
    list.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: "nearest" });
  });
  const choose = (type: SupplierType | undefined) => {
    if (type && !unsellable(type)) onPick(type);
  };
  const keys = (event: KeyboardEvent<HTMLInputElement>) => {
    const last = rows.length - 1;
    const to = { ArrowDown: Math.min(active + 1, last), ArrowUp: Math.max(active - 1, 0), Home: 0, End: last }[
      event.key
    ];
    if (to !== undefined && rows.length > 0) {
      event.preventDefault();
      setAt(to);
    } else if (event.key === "Enter" && !event.nativeEvent.isComposing) {
      event.preventDefault();
      choose(rows[active]);
    }
  };
  return (
    <div className={s.root}>
      <Field
        ref={input}
        label={t("upstream.types.search")}
        placeholder={t("upstream.types.placeholder")}
        value={query}
        onChange={(event) => {
          setQuery(event.target.value);
          setAt(0);
        }}
        onKeyDown={keys}
        role="combobox"
        aria-expanded="true"
        aria-controls={`${id}-list`}
        aria-activedescendant={rows.length > 0 ? `${id}-${active}` : undefined}
        autoComplete="off"
        spellCheck={false}
      />
      {rows.length === 0 ? (
        <p className={s.empty} role="status">
          {t("upstream.types.empty", { query: query.trim() })}
        </p>
      ) : (
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
                    active={n === active}
                    onHover={() => setAt(n)}
                    onChoose={() => choose(type)}
                  />
                );
              })}
            </div>
          ))}
        </div>
      )}
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
      <VendorIcon vendor={type.vendor} name={type.name} />
      <span className={s.text}>
        <span className={s.name}>{type.name}</span>
        <span className={s.line}>{greyed ? t("upstream.types.noModels") : typeLine(type, t)}</span>
      </span>
      {count === undefined ? null : <span className={s.count}>{t("upstream.types.models", { count })}</span>}
    </div>
  );
}

// The type chosen, over the fields it asks for, with the way back to the picker.
export function ChosenType({ type, onChange }: { type: SupplierType; onChange: () => void }) {
  const { t } = useTranslation();
  return (
    <div className={s.chosen}>
      <VendorIcon vendor={type.vendor} name={type.name} />
      <span className={s.text}>
        <span className={s.name}>{type.name}</span>
        <span className={s.line}>{typeLine(type, t)}</span>
      </span>
      <ActionButton size="sm" variant="gho" onClick={onChange}>
        {t("upstream.types.change")}
      </ActionButton>
    </div>
  );
}
