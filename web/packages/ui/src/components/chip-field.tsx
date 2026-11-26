import { type KeyboardEvent, useRef, useState } from "react";
import { classes } from "../classes";
import s from "./chip-field.module.css";
import { Icon } from "./icon";

// A comma in either width ends a chip as Enter does, so a Chinese keyboard's "，" works as well as ",".
const ends = new Set(["Enter", ",", "，"]);

// ChipField is a list of short values edited in place (the chips setting, adCtl and adA_chipx in admin_set.js): the
// values as chips, each with an × to take it out, and a box after them; Enter or a comma adds what is typed, trimmed,
// unless it is empty or already there. Focus stays in the box when a chip goes, so the list can be edited from the keys.
// label is the box's accessible name (the row names the setting); removeLabel names each chip's ×.
export function ChipField({
  label,
  values,
  onChange,
  removeLabel,
  placeholder,
  mono = false,
  disabled = false,
}: {
  label: string;
  values: readonly string[];
  onChange: (values: string[]) => void;
  removeLabel: (value: string) => string;
  placeholder?: string;
  mono?: boolean;
  disabled?: boolean;
}) {
  const box = useRef<HTMLInputElement>(null);
  const [draft, setDraft] = useState("");
  const add = (event: KeyboardEvent<HTMLInputElement>) => {
    if (!ends.has(event.key) || event.nativeEvent.isComposing) return;
    event.preventDefault();
    const value = draft.trim();
    if (value && !values.includes(value)) onChange([...values, value]);
    setDraft("");
  };
  const remove = (at: number) => {
    onChange(values.filter((_, i) => i !== at));
    box.current?.focus();
  };
  return (
    <div className={s.root}>
      {values.map((value, at) => (
        <span key={value} className={classes(s.chip, mono && s.mono)}>
          {value}
          <button
            type="button"
            className={s.remove}
            aria-label={removeLabel(value)}
            disabled={disabled}
            onClick={() => remove(at)}
          >
            <Icon name="close" />
          </button>
        </span>
      ))}
      <input
        ref={box}
        className={s.input}
        aria-label={label}
        placeholder={placeholder}
        spellCheck={false}
        autoComplete="off"
        value={draft}
        disabled={disabled}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={add}
      />
    </div>
  );
}
