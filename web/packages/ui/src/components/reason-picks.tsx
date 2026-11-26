import { useRef } from "react";
import { TextArea } from "./field";
import s from "./reason-picks.module.css";

// ReasonPicks is a change's reason (the reason field and adReasons in admin_core.js): a textarea and, under it, the
// common reasons as chips. A chip puts its words in the box unless they are already there, as the board does, and hands
// focus back to the box to go on writing. Each chip's own box is what takes the press; on a phone it reaches 44px
// around the chip itself.
export function ReasonPicks({
  label,
  picksLabel,
  picks,
  value,
  onChange,
  placeholder,
  hint,
  error,
  disabled = false,
}: {
  label: string;
  picksLabel: string;
  picks: readonly string[];
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  hint?: string;
  error?: string;
  disabled?: boolean;
}) {
  const box = useRef<HTMLTextAreaElement>(null);
  const pick = (reason: string) => {
    if (!value.includes(reason)) onChange(reason);
    box.current?.focus();
  };
  return (
    <div className={s.root}>
      <TextArea
        ref={box}
        label={label}
        hint={hint}
        error={error}
        placeholder={placeholder}
        value={value}
        disabled={disabled}
        onChange={(event) => onChange(event.target.value)}
      />
      <fieldset aria-label={picksLabel} className={s.picks}>
        {picks.map((reason) => (
          <button key={reason} type="button" className={s.pick} disabled={disabled} onClick={() => pick(reason)}>
            {reason}
          </button>
        ))}
      </fieldset>
    </div>
  );
}
