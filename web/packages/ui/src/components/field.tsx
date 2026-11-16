import { type ComponentProps, type ReactNode, useId } from "react";
import { classes } from "../classes";
import s from "./field.module.css";

// The fields of a form. A field's label, hint and error are tied to its control (for, aria-describedby,
// aria-invalid), so a screen reader reads them with it. An error never clears what was typed: the field only marks it,
// and the value stays the caller's.
type Frame = { label: string; hint?: string; error?: string };
type Native<E extends "input" | "textarea" | "select"> = Omit<ComponentProps<E>, "id" | "className" | "style">;

function FieldFrame({ label, hint, error, control }: Frame & { control: (aria: Aria) => ReactNode }) {
  const id = useId();
  const described = [hint && `${id}-hint`, error && `${id}-error`].filter(Boolean).join(" ");
  return (
    <div className={s.root}>
      <label htmlFor={id} className={s.label}>
        {label}
      </label>
      {control({ id, "aria-describedby": described || undefined, "aria-invalid": error ? true : undefined })}
      {hint ? (
        <span id={`${id}-hint`} className={s.hint}>
          {hint}
        </span>
      ) : null}
      {error ? (
        <span id={`${id}-error`} className={s.error}>
          {error}
        </span>
      ) : null}
    </div>
  );
}

type Aria = { id: string; "aria-describedby"?: string; "aria-invalid"?: true };

export function Field({ label, hint, error, ...props }: Frame & Native<"input">) {
  return (
    <FieldFrame
      label={label}
      hint={hint}
      error={error}
      control={(aria) => <input className={s.control} {...props} {...aria} />}
    />
  );
}

export function TextArea({ label, hint, error, ...props }: Frame & Native<"textarea">) {
  return (
    <FieldFrame
      label={label}
      hint={hint}
      error={error}
      control={(aria) => <textarea className={classes(s.control, s.area)} {...props} {...aria} />}
    />
  );
}

export function Select({
  label,
  hint,
  error,
  options,
  ...props
}: Frame & Native<"select"> & { options: readonly { value: string; label: string }[] }) {
  return (
    <FieldFrame
      label={label}
      hint={hint}
      error={error}
      control={(aria) => (
        <select className={classes(s.control, s.select)} {...props} {...aria}>
          {options.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      )}
    />
  );
}
