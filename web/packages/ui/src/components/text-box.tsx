import { useEffect, useRef } from "react";
import { classes } from "../classes";
import s from "./text-box.module.css";

// TextBox is a setting's text in its row (.adinp, .mono and .adcol in admin_set.js): a box of the given width, mono for
// an address or a key; with a swatch it is a colour, the swatch showing what is typed. The row names the setting, so
// label is the box's accessible name, not a visible label.
export function TextBox({
  label,
  value,
  onChange,
  width = 200,
  mono = false,
  swatch,
  type = "text",
  placeholder,
  maxLength,
  invalid = false,
  disabled = false,
  focused = false,
  onBlur,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  width?: number;
  mono?: boolean;
  // A colour to draw before the text; an unusable one draws nothing.
  swatch?: string;
  type?: "text" | "password" | "email" | "url";
  placeholder?: string;
  maxLength?: number;
  invalid?: boolean;
  disabled?: boolean;
  // Takes focus as it appears, for a box opened by a press.
  focused?: boolean;
  onBlur?: () => void;
}) {
  const input = useRef<HTMLInputElement>(null);
  // Once, as the box appears: a later render must not take the focus back from where the person went.
  useEffect(() => {
    if (focused) input.current?.focus();
  }, []);
  return (
    <label
      className={classes(s.root, mono && s.mono, invalid && s.invalid)}
      style={{ "--w": `${width}px` } as Record<`--${string}`, string>}
    >
      {swatch === undefined ? null : (
        <i className={s.swatch} aria-hidden="true" style={{ "--c": swatch } as Record<`--${string}`, string>} />
      )}
      <input
        ref={input}
        className={s.input}
        type={type}
        aria-label={label}
        aria-invalid={invalid || undefined}
        autoComplete={type === "password" ? "new-password" : "off"}
        spellCheck={false}
        placeholder={placeholder}
        maxLength={maxLength}
        value={value}
        disabled={disabled}
        onChange={(event) => onChange(event.target.value)}
        onBlur={onBlur}
      />
    </label>
  );
}
