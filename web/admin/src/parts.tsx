import { can, readForm, useSession } from "@axiom/app";
import { CodeBlock } from "@axiom/boards/ds";
import { ActionButton, Card, Cluster, Grid, Select, Stack, Tag, Text, Th, useFormat } from "@axiom/ui";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { formatJSON } from "./lossless";

export { toInstant, toLocalInput } from "@axiom/app";
// Under the names the admin's pages use: a row of the things a page offers (Actions), what is known about one thing
// (Facts), and an identifier or name the system gives (Code).
export { Cluster as Actions, Facts, Mono as Code, PageBody } from "@axiom/ui";

// SubLine is a cell's second line: when, through which merchant, the quote beside an amount, a provider's reference.
export function SubLine({ children }: { children: ReactNode }) {
  return (
    <Text as="div" size="sm" tone="faint">
      {children}
    </Text>
  );
}

// Time is a moment in the viewer's zone, or a dash for none.
export function Time({ at }: { at: string | undefined }) {
  const format = useFormat();
  return at ? format.dateTime(new Date(at)) : "—";
}

// Json keeps a record's shape: the route of a call and an audit entry's detail are nested and meant to be read as they
// were written, not flattened into one line. The block scrolls inside itself and takes focus to be scrolled by keyboard.
export function Json({ value, label }: { value: unknown; label: string }) {
  return <CodeBlock label={label}>{formatJSON(value)}</CodeBlock>;
}

// A state in words with an icon: the tone says how much attention it needs, never alone.
const tones = {
  neutral: ["neutral", undefined],
  good: ["ok", "check"],
  warn: ["warn", "alert"],
  bad: ["bad", "alert"],
} as const;

export function State({ tone, children }: { tone: keyof typeof tones; children: ReactNode }) {
  const [tag, icon] = tones[tone];
  return (
    <Tag tone={tag} icon={icon}>
      {children}
    </Tag>
  );
}

// FilterBar narrows a list by what is typed; the filters live in the address, so a reload, the back button and a link
// keep them. The form is rebuilt when they change from outside it. Its fields sit as many to a row as fit, top-aligned,
// so a field's hint never pushes its neighbours out of line; the buttons get a row of their own.
export function FilterBar({
  values,
  onApply,
  children,
}: {
  values: object;
  onApply: (values: Record<string, string>) => void;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <Card>
      <form
        key={JSON.stringify(values)}
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          onApply(readForm(event.currentTarget));
        }}
      >
        <Stack>
          <Grid min="16rem">{children}</Grid>
          <Cluster>
            <ActionButton variant="pri" type="submit">
              {t("filter.apply")}
            </ActionButton>
            <ActionButton onClick={() => onApply({})}>{t("filter.reset")}</ActionButton>
          </Cluster>
        </Stack>
      </form>
    </Card>
  );
}

// text is what a filter field holds: nothing typed does not narrow.
export function text(value: string | undefined) {
  const trimmed = value?.trim();
  return trimmed ? trimmed : undefined;
}

// Head is the header row of a table: a label per column, the column that names a row marked main, and an empty last
// column for the buttons of a row. A table too wide for the window scrolls inside its box; no column is hidden.
type HeadColumn = string | [label: string, layout?: { main?: boolean; align?: "end"; shrink?: boolean }];

export function Head({ cols, action = false }: { cols: HeadColumn[]; action?: boolean }) {
  return (
    <thead>
      <tr>
        {cols.map((col) => {
          const [label, layout] = typeof col === "string" ? [col, undefined] : col;
          return (
            <Th key={label} {...layout}>
              {label}
            </Th>
          );
        })}
        {action ? <Th actions /> : null}
      </tr>
    </thead>
  );
}

// ChoiceFilter is a filter that picks one of a few values, or none.
export function ChoiceFilter({
  label,
  name,
  value,
  any,
  options,
}: {
  label: string;
  name: string;
  value: string | undefined;
  any: string;
  options: readonly { value: string; label: string }[];
}) {
  return (
    <Select label={label} name={name} defaultValue={value ?? ""} options={[{ value: "", label: any }, ...options]} />
  );
}

// NewButton is the button that opens a dialog for making a thing, shown to a session that may make it.
export function NewButton({
  permission,
  label,
  children,
}: {
  permission: string;
  label: string;
  children: (close: () => void) => ReactNode;
}) {
  const allowed = can(useSession(), permission);
  const [open, setOpen] = useState(false);
  if (!allowed) {
    return null;
  }
  return (
    <>
      <ActionButton variant="pri" icon="plus" onClick={() => setOpen(true)}>
        {label}
      </ActionButton>
      {open ? children(() => setOpen(false)) : null}
    </>
  );
}

// Whether a section may appear: once what is above it has first had an answer, and from then on, so a retry or a new
// filter above it never unmounts the section and loses what it holds. Sections that appeared before the answer came
// were pushed down the screen when it grew (#747).
export function useShownAfter(pending: boolean) {
  const [shown, setShown] = useState(false);
  if (!shown && !pending) {
    setShown(true);
  }
  return shown;
}
