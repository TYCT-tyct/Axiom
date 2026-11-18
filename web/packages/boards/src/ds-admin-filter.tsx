import { useState } from "react";
import Frame from "./AxApp/regions/PartFilterBar.tsx";
import { ActionButton } from "./ds-admin-actions.tsx";
import { AdminInput, SelectField } from "./ds-admin-fields.tsx";

type Field = { name: string; label: string; hint?: string };
type Filter =
  | (Field & { type: "text"; placeholder?: string; maxLength?: number })
  | (Field & { type: "select"; options: readonly { value: string; label: string }[] });
type Values = Readonly<Record<string, string | undefined>>;
type Props = {
  label: string;
  fields: readonly Filter[];
  range?: { from: Field; until: Field };
  values: Values;
  applyLabel: string;
  resetLabel: string;
  onApply: (values: Record<string, string>) => void;
};

// The route supplies applied values and its navigation callback. Date fields use the viewer's local clock so the
// route can reuse its existing conversion when reading and writing instants in the address.
export function FilterForm(props: Props) {
  return <Draft key={JSON.stringify(props.values)} {...props} />;
}

function Draft({ label, fields, range, values, applyLabel, resetLabel, onApply }: Props) {
  const [draft, setDraft] = useState(values);
  const control = ({ name, label, hint }: Field) => ({
    name,
    label,
    hint,
    value: draft[name] ?? "",
    onChange: (value: string) => setDraft((current) => ({ ...current, [name]: value })),
  });
  return (
    <form
      noValidate
      onSubmit={(event) => {
        event.preventDefault();
        onApply(Object.fromEntries(new FormData(event.currentTarget)) as Record<string, string>);
      }}
    >
      <Frame t={String} values={{ label }}>
        {fields.map((field) =>
          field.type === "select" ? (
            <SelectField key={field.name} {...control(field)} options={field.options} />
          ) : (
            <AdminInput
              key={field.name}
              {...control(field)}
              placeholder={field.placeholder}
              maxLength={field.maxLength}
            />
          ),
        )}
        {range && (
          <>
            <AdminInput {...control(range.from)} inputType="datetime-local" />
            <AdminInput {...control(range.until)} inputType="datetime-local" />
          </>
        )}
        <ActionButton variant="primary" type="submit">
          {applyLabel}
        </ActionButton>
        <ActionButton
          onClick={() => {
            setDraft({});
            onApply({});
          }}
        >
          {resetLabel}
        </ActionButton>
      </Frame>
    </form>
  );
}
