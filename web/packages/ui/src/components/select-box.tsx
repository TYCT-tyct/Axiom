import s from "./select-box.module.css";

// SelectBox is a setting's choice among more options than a segmented control holds on a phone: a native select in the
// box of a setting's text (TextBox). The row names the setting, so label is its accessible name.
export function SelectBox<T extends string>({
  label,
  value,
  options,
  onChange,
  disabled = false,
}: {
  label: string;
  value: T;
  options: readonly { value: T; label: string }[];
  onChange: (value: T) => void;
  disabled?: boolean;
}) {
  return (
    <label className={s.root}>
      <select
        className={s.input}
        aria-label={label}
        value={value}
        disabled={disabled}
        onChange={(event) => onChange(event.target.value as T)}
      >
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
    </label>
  );
}
