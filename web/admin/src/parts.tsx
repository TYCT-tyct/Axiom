import { can, readForm, useSession } from "@axiom/app";
import { CodeBlock } from "@axiom/boards/ds";
import {
  ActionButton,
  Card,
  Cluster,
  Facts as FactList,
  Grid,
  Mono,
  Select,
  Stack,
  Tag,
  Th,
  useFormat,
} from "@axiom/ui";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { formatJSON } from "./lossless";

export { toInstant, toLocalInput } from "@axiom/app";
export { PageBody } from "@axiom/ui";

// A row of the things a page offers, such as its creating button.
export function Actions({ children }: { children: ReactNode }) {
  return <Cluster>{children}</Cluster>;
}

// Facts is what is known about one thing, label beside value; a value may be as long as the contract allows, so it
// wraps anywhere.
export function Facts({ title, action, rows }: { title: string; action?: ReactNode; rows: [string, ReactNode][] }) {
  return <FactList title={title} actions={action} rows={rows} />;
}

// Time is a moment in the viewer's zone, or a dash for none.
export function Time({ at }: { at: string | undefined }) {
  const format = useFormat();
  return at ? format.dateTime(new Date(at)) : "—";
}

// Code is an identifier or a name the system gives, in the monospaced face; it may be long, so it wraps anywhere.
export function Code({ children, title }: { children: ReactNode; title?: string }) {
  return <Mono title={title}>{children}</Mono>;
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
