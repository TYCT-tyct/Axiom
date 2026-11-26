import type { KeyboardEvent } from "react";
import { classes } from "../classes";
import s from "./number-field.module.css";

type Steps = { step: number; decimals: number; min?: number; max?: number; shift: boolean };

// stepped is the value one press of up or down leaves: step (ten steps with Shift) from what is typed, kept within min
// and max and written with the field's decimals; undefined when what is typed is not a number, which stays as it is.
function stepped(
  text: string,
  by: 1 | -1,
  { step, decimals, min = -Infinity, max = Infinity, shift }: Steps,
): string | undefined {
  const now = Number(text);
  if (text.trim() === "" || !Number.isFinite(now)) return undefined;
  const next = Math.min(max, Math.max(min, now + by * step * (shift ? 10 : 1)));
  return next.toFixed(decimals);
}

// NumberField is a setting's number in its row (.adinp with inputmode decimal in admin_set.js): right aligned, with an
// optional prefix and unit inside the box. Up and down step it (by default one unit of its last decimal), Shift for ten
// steps. The value is the typed text, so a half-typed number is never rewritten; checking it is the page's. The row
// names the setting, so label is the field's accessible name, not a visible label.
export function NumberField({
  label,
  value,
  onChange,
  unit,
  prefix,
  decimals = 0,
  step = 10 ** -decimals,
  min,
  max,
  invalid = false,
  disabled = false,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  unit?: string;
  prefix?: string;
  step?: number;
  decimals?: number;
  min?: number;
  max?: number;
  invalid?: boolean;
  disabled?: boolean;
}) {
  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    const by = event.key === "ArrowUp" ? 1 : event.key === "ArrowDown" ? -1 : 0;
    if (!by) return;
    const next = stepped(value, by, { step, decimals, min, max, shift: event.shiftKey });
    if (next === undefined) return;
    event.preventDefault();
    onChange(next);
  };
  return (
    <label className={classes(s.root, (unit?.length ?? 0) > 2 && s.long, invalid && s.invalid)}>
      {prefix ? <span className={s.prefix}>{prefix}</span> : null}
      <input
        className={s.input}
        inputMode="decimal"
        autoComplete="off"
        aria-label={label}
        aria-invalid={invalid || undefined}
        value={value}
        disabled={disabled}
        onChange={(event) => onChange(event.target.value)}
        onKeyDown={onKeyDown}
      />
      {unit ? <span className={s.unit}>{unit}</span> : null}
    </label>
  );
}
