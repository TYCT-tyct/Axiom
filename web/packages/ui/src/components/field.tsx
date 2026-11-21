import { type ComponentProps, type ReactNode, useId } from "react";
import { useTranslation } from "react-i18next";
import { board } from "../board-classes";
import { classes } from "../classes";
import { useFormat } from "../format";
import s from "./field.module.css";
import { Icon } from "./icon";

// The fields of a form (.qfl/.qin from AxApp/regions.css). A field's label, hint and error are tied to its control
// (for, aria-describedby, aria-invalid), so a screen reader reads them with it. An error never clears what was typed:
// the field only marks it, and the value stays the caller's.
type Frame = { label: string; hint?: string; error?: string };
type Native<E extends "input" | "textarea" | "select"> = Omit<ComponentProps<E>, "id" | "className" | "style">;

function FieldFrame({ label, hint, error, control }: Frame & { control: (aria: Aria) => ReactNode }) {
  const id = useId();
  const described = [hint && `${id}-hint`, error && `${id}-error`].filter(Boolean).join(" ");
  return (
    <div className={classes(board.qfl, s.root)}>
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
      control={(aria) => <input className={classes(board.qin, s.control)} {...props} {...aria} />}
    />
  );
}

export function TextArea({ label, hint, error, ...props }: Frame & Native<"textarea">) {
  return (
    <FieldFrame
      label={label}
      hint={hint}
      error={error}
      control={(aria) => <textarea className={classes(board.qin, s.control, s.area)} {...props} {...aria} />}
    />
  );
}

// Select offers its options as data, or as the option elements it is given; a select has no arrow, as on the board.
export function Select({
  label,
  hint,
  error,
  options,
  children,
  ...props
}: Frame &
  Native<"select"> &
  (
    | { options: readonly { value: string; label: string }[]; children?: never }
    | { options?: never; children: ReactNode }
  )) {
  return (
    <FieldFrame
      label={label}
      hint={hint}
      error={error}
      control={(aria) => (
        <select className={classes(board.qin, s.control, s.select)} {...props} {...aria}>
          {options
            ? options.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}
                </option>
              ))
            : children}
        </select>
      )}
    />
  );
}

// CheckField is a checkbox with its words beside it and an optional hint under them.
export function CheckField({ label, hint, hidden, ...props }: Omit<Frame, "error"> & Omit<Native<"input">, "type">) {
  const id = useId();
  return (
    <div className={s.check} hidden={hidden}>
      <input id={id} type="checkbox" aria-describedby={hint ? `${id}-hint` : undefined} className={s.box} {...props} />
      <div className={s.checkText}>
        <label htmlFor={id} className={s.checkLabel}>
          {label}
        </label>
        {hint ? (
          <span id={`${id}-hint`} className={s.hint}>
            {hint}
          </span>
        ) : null}
      </div>
    </div>
  );
}

// MonthField picks a calendar month, "YYYY-MM", with the month's name and the year in the page's language. The browser's
// own month input takes its month name from the browser's locale, which is not the page's; two selects are the same
// control in every browser. The months are the contract's calendar months, not the viewer's zone's: a target covers a
// UTC month. The years around the chosen one are offered so the control stays short; the chosen year is always among
// them, and selecting a boundary year adds the next year outward, so the range can keep growing to any month the
// server accepts.
export function MonthField({
  label,
  hint,
  value,
  onChange,
}: {
  label: string;
  hint?: string;
  value: string;
  onChange: (value: string) => void;
}) {
  const { t } = useTranslation("common");
  const format = useFormat();
  const id = useId();
  const [yearText = "", monthText = ""] = value.split("-");
  const year = Number(yearText);
  const current = new Date().getUTCFullYear();
  const first = Math.min(year, current - 10);
  const last = Math.max(year, current + 10);
  const years = Array.from({ length: last - first + 1 }, (_, index) => first + index);
  if (year === first) {
    years.unshift(first - 1);
  }
  if (year === last) {
    years.push(last + 1);
  }
  const describedBy = hint ? `${id}-hint` : undefined;
  return (
    <fieldset className={s.month}>
      <legend id={id} className={s.label}>
        {label}
      </legend>
      <div className={s.monthControls}>
        <select
          aria-label={t("month.month")}
          aria-describedby={describedBy}
          className={classes(board.qin, s.control, s.select)}
          value={monthText}
          onChange={(event) => onChange(`${yearText}-${event.target.value}`)}
        >
          {Array.from({ length: 12 }, (_, index) => index + 1).map((month) => (
            <option key={month} value={String(month).padStart(2, "0")}>
              {format.monthName(month)}
            </option>
          ))}
        </select>
        <select
          aria-label={t("month.year")}
          aria-describedby={describedBy}
          className={classes(board.qin, s.control, s.select)}
          value={yearText}
          onChange={(event) => onChange(`${event.target.value}-${monthText}`)}
        >
          {years.map((option) => (
            <option key={option} value={option}>
              {option}
            </option>
          ))}
        </select>
      </div>
      {hint ? (
        <span id={`${id}-hint`} className={s.hint}>
          {hint}
        </span>
      ) : null}
    </fieldset>
  );
}

// FormError says what went wrong with the whole form, where the user is looking; it is announced when it appears.
export function FormError({ children }: { children: ReactNode }) {
  return (
    <div role="alert" className={s.formError}>
      <Icon name="alert" className={s.formErrorIcon} />
      <div className={s.formErrorText}>{children}</div>
    </div>
  );
}
