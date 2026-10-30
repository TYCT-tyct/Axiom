import { can, readForm, useSession } from "@axiom/app";
import { Badge, Button, Icon, Panel, SelectField, Th, useFormat } from "@axiom/ui";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";

export { toInstant, toLocalInput } from "@axiom/app";

// PageBody is the area under a page's header; the pages of the admin are all lists, cards and tables in a column.
export function PageBody({ children }: { children: ReactNode }) {
  return <div className="space-y-4 p-4 sm:p-6">{children}</div>;
}

// A row of the things a page offers, such as its creating button.
export function Actions({ children }: { children: ReactNode }) {
  return <div className="flex flex-wrap items-center gap-2">{children}</div>;
}

// Facts is what is known about one thing, label beside value; a value may be as long as the contract allows, so it
// wraps anywhere.
export function Facts({ title, action, rows }: { title: string; action?: ReactNode; rows: [string, ReactNode][] }) {
  return (
    <Panel title={title} action={action}>
      <dl className="divide-y divide-b1">
        {rows.map(([label, value]) => (
          <div key={label} className="grid gap-x-4 gap-y-1 px-4 py-3 sm:grid-cols-[12rem_minmax(0,1fr)]">
            <dt className="text-t3">{label}</dt>
            <dd className="min-w-0 text-t1 [overflow-wrap:anywhere]">{value}</dd>
          </div>
        ))}
      </dl>
    </Panel>
  );
}

// Time is a moment in the viewer's zone, or a dash for none.
export function Time({ at }: { at: string | undefined }) {
  const format = useFormat();
  return at ? format.dateTime(new Date(at)) : "—";
}

// Code is an identifier or a name the system gives, in the monospaced face; it may be long, so it wraps anywhere.
export function Code({ children }: { children: ReactNode }) {
  return <span className="font-mono text-label [overflow-wrap:anywhere]">{children}</span>;
}

// Json keeps a record's shape: the route of a call and an audit entry's detail are nested and meant to be read as they
// were written, not flattened into one line.
export function Json({ value }: { value: unknown }) {
  return (
    <pre className="max-h-96 overflow-auto rounded-md bg-s2 p-3 font-mono text-label text-t2">
      {JSON.stringify(value, null, 2)}
    </pre>
  );
}

// A state in words with an icon: the tone says how much attention it needs, never alone.
const tones = {
  neutral: ["neutral", undefined],
  good: ["ok", "check"],
  warn: ["warn", "alert"],
  bad: ["bad", "alert"],
} as const;

export function State({ tone, children }: { tone: keyof typeof tones; children: ReactNode }) {
  const [badge, icon] = tones[tone];
  return (
    <Badge tone={badge} icon={icon}>
      {children}
    </Badge>
  );
}

// FilterBar narrows a list by what is typed; the filters live in the address, so a reload, the back button and a link
// keep them. The form is rebuilt when they change from outside it.
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
    <Panel className="p-4">
      <form
        key={JSON.stringify(values)}
        noValidate
        className="grid gap-4 sm:grid-cols-2 xl:grid-cols-[repeat(3,minmax(0,1fr))_auto] xl:items-end"
        onSubmit={(event) => {
          event.preventDefault();
          onApply(readForm(event.currentTarget));
        }}
      >
        {children}
        <div className="flex gap-2">
          <Button variant="primary" size="md" type="submit">
            {t("filter.apply")}
          </Button>
          <Button size="md" onClick={() => onApply({})}>
            {t("filter.reset")}
          </Button>
        </div>
      </form>
    </Panel>
  );
}

// text is what a filter field holds: nothing typed does not narrow.
export function text(value: string | undefined) {
  const trimmed = value?.trim();
  return trimmed ? trimmed : undefined;
}

// mainCell is the class of the cell that names a row. On a phone it takes the room left by the others and truncates,
// so the row's buttons stay on screen; from sm up it keeps a readable width.
export const mainCell = "w-full max-w-0 sm:w-auto sm:max-w-72 sm:min-w-40";

// Head is the header row of a table: a label per column, with the classes that hide a column on a narrow window, and an
// empty last column for the buttons of a row.
export function Head({ cols, action = false }: { cols: [label: string, className?: string][]; action?: boolean }) {
  return (
    <thead>
      <tr>
        {cols.map(([label, className]) => (
          <Th key={label} className={className}>
            {label}
          </Th>
        ))}
        {action ? <Th className="w-0" /> : null}
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
    <SelectField label={label} name={name} defaultValue={value ?? ""}>
      <option value="">{any}</option>
      {options.map((option) => (
        <option key={option.value} value={option.value}>
          {option.label}
        </option>
      ))}
    </SelectField>
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
      <Button variant="primary" size="md" onClick={() => setOpen(true)}>
        <Icon name="plus" />
        {label}
      </Button>
      {open ? children(() => setOpen(false)) : null}
    </>
  );
}
