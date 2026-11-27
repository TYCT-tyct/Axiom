import { ActionButton, Cluster, Field, type RowItem, RowList, Stack, Text } from "@axiom/ui";
import type { TFunction } from "i18next";
import { type KeyboardEvent, type RefObject, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { VendorIcon } from "./icons/vendor-icon";
import { findTypes, modelCount, type SupplierType, typeLine, unsellable } from "./supplier-types";

// The type picker of 添加上游账号: a query over the types, listed under their groups' headings, each a button. Enter in
// the query takes the first type found; the arrow keys, Home and End move between the types (and up out of the first,
// back to the query). A type with nothing to sell yet is a disabled button whose line says why.
export function TypePicker({
  types,
  onPick,
}: {
  types: readonly SupplierType[];
  onPick: (type: SupplierType) => void;
}) {
  const { t } = useTranslation();
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const [query, setQuery] = useState("");
  const sections = useMemo(() => findTypes(types, query), [types, query]);
  useEffect(() => input.current?.focus(), []);
  const move = moveFocus(input, list);
  return (
    <Stack gap={8}>
      <Field
        ref={input}
        label={t("upstream.types.search")}
        placeholder={t("upstream.types.placeholder")}
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter" && !event.nativeEvent.isComposing) {
            event.preventDefault();
            enabled(list.current)[0]?.click();
          } else if (event.key === "ArrowDown") move(event);
        }}
        autoComplete="off"
        spellCheck={false}
      />
      {sections.length === 0 ? (
        <Text tone="muted">{t("upstream.types.empty", { query: query.trim() })}</Text>
      ) : (
        // biome-ignore lint/a11y/useSemanticElements: it groups buttons for the arrow keys; a fieldset groups a form's fields.
        <div ref={list} role="group" aria-label={t("upstream.types.label")} onKeyDown={move}>
          <Stack gap={12}>
            {sections.map((section) => (
              <Stack key={section.group} gap={4}>
                <Text as="h3" size="sm" tone="faint">
                  {t(`upstream.types.groups.${section.group}`)}
                </Text>
                <RowList
                  label={t(`upstream.types.groups.${section.group}`)}
                  items={section.types.map((type) => typeRow(type, t, onPick))}
                />
              </Stack>
            ))}
          </Stack>
        </div>
      )}
    </Stack>
  );
}

const enabled = (list: HTMLElement | null) => [
  ...(list?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") ?? []),
];

// The arrow keys, Home and End move focus between the types that can be taken; up from the first goes to the query.
function moveFocus(input: RefObject<HTMLInputElement | null>, list: RefObject<HTMLDivElement | null>) {
  return (event: KeyboardEvent) => {
    const all = enabled(list.current);
    const at = all.indexOf(document.activeElement as HTMLButtonElement);
    const to = ({ ArrowDown: at + 1, ArrowUp: at - 1, Home: 0, End: all.length - 1 } as Record<string, number>)[
      event.key
    ];
    if (to === undefined || all.length === 0) return;
    event.preventDefault();
    if (to < 0) input.current?.focus();
    else all[Math.min(to, all.length - 1)]?.focus();
  };
}

function typeRow(type: SupplierType, t: TFunction<"admin">, onPick: (type: SupplierType) => void): RowItem {
  const greyed = unsellable(type);
  const count = modelCount(type);
  return {
    key: type.kind,
    lead: <VendorIcon vendor={type.vendor} name={type.name} />,
    title: type.name,
    sub: greyed ? t("upstream.types.noModels") : typeLine(type, t),
    end: count === undefined ? undefined : t("upstream.types.models", { count }),
    link: <button type="button" disabled={greyed} onClick={() => onPick(type)} />,
  };
}

// The type chosen, over the fields it asks for, with the way back to the picker.
export function ChosenType({ type, onChange }: { type: SupplierType; onChange: () => void }) {
  const { t } = useTranslation();
  return (
    <Cluster justify="between">
      <Cluster gap={12}>
        <VendorIcon vendor={type.vendor} name={type.name} />
        <Stack gap={4}>
          <Text medium>{type.name}</Text>
          <Text size="sm" tone="muted">
            {typeLine(type, t)}
          </Text>
        </Stack>
      </Cluster>
      <ActionButton size="sm" variant="gho" onClick={onChange}>
        {t("upstream.types.change")}
      </ActionButton>
    </Cluster>
  );
}
