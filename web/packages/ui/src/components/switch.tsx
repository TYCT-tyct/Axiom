import s from "./switch.module.css";

// Switch turns one setting on or off at once; a choice that waits for a save is a checkbox in a form instead. Its
// label names it for a screen reader, which also hears whether it is on. The hit area is 44px whatever its size.
export function Switch({
  label,
  checked,
  disabled = false,
  onChange,
}: {
  label: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      className={s.root}
      onClick={() => onChange(!checked)}
    >
      <span className={s.knob} />
    </button>
  );
}
