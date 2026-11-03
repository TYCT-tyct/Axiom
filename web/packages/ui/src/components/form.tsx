import { type ComponentProps, type ReactNode, useId } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "../cn";
import { useFormat } from "../format";
import { Icon } from "./icon";

// The pieces every form is made of. A field's label, hint and error are tied to its control (for, aria-describedby,
// aria-invalid), so a screen reader reads them with it; an error shows as words, never as a colour alone. A control is
// 16px on a phone, below which iOS zooms the page when it takes focus.

const control =
  "w-full rounded-btn border border-b2 bg-s1 px-3 text-base text-t1 shadow-panel transition-colors duration-150 placeholder:text-t4 hover:border-b3 disabled:opacity-50 aria-invalid:border-bad sm:text-ui";

type FieldProps = { label: string; hint?: string; error?: string };

function FieldFrame({
  id,
  label,
  hint,
  error,
  children,
}: FieldProps & { id: string; children: (describedBy: string | undefined) => ReactNode }) {
  const describedBy = [hint ? `${id}-hint` : null, error ? `${id}-error` : null].filter(Boolean).join(" ");
  return (
    <div className="space-y-1.5">
      <label htmlFor={id} className="block text-ui font-medium text-t2">
        {label}
      </label>
      {children(describedBy || undefined)}
      {hint ? (
        <p id={`${id}-hint`} className="text-label text-t4">
          {hint}
        </p>
      ) : null}
      {error ? (
        <p id={`${id}-error`} className="text-label text-bad">
          {error}
        </p>
      ) : null}
    </div>
  );
}

export function TextField({
  label,
  hint,
  error,
  className,
  ...props
}: FieldProps & Omit<ComponentProps<"input">, "id">) {
  const id = useId();
  return (
    <FieldFrame id={id} label={label} hint={hint} error={error}>
      {(describedBy) => (
        <input
          id={id}
          aria-describedby={describedBy}
          aria-invalid={error ? true : undefined}
          className={cn(control, "h-10 sm:h-9", className)}
          {...props}
        />
      )}
    </FieldFrame>
  );
}

export function TextAreaField({
  label,
  hint,
  error,
  className,
  ...props
}: FieldProps & Omit<ComponentProps<"textarea">, "id">) {
  const id = useId();
  return (
    <FieldFrame id={id} label={label} hint={hint} error={error}>
      {(describedBy) => (
        <textarea
          id={id}
          aria-describedby={describedBy}
          aria-invalid={error ? true : undefined}
          className={cn(control, "min-h-20 py-2 font-mono", className)}
          {...props}
        />
      )}
    </FieldFrame>
  );
}

export function SelectField({
  label,
  hint,
  error,
  className,
  ...props
}: FieldProps & Omit<ComponentProps<"select">, "id">) {
  const id = useId();
  return (
    <FieldFrame id={id} label={label} hint={hint} error={error}>
      {(describedBy) => (
        <select
          id={id}
          aria-describedby={describedBy}
          aria-invalid={error ? true : undefined}
          className={cn(control, "h-10 sm:h-9", className)}
          {...props}
        />
      )}
    </FieldFrame>
  );
}

// MonthField picks a calendar month, "YYYY-MM", with the month's name and the year in the page's language. The browser's
// own month input takes its month name from the browser's locale, which is not the page's; two selects are the same
// control in every browser. The months are the contract's calendar months, not the viewer's zone's: a target covers a
// UTC month. The years around the chosen one are offered so the control stays short; the chosen year is always among
// them, and the server takes any month.
export function MonthField({
  label,
  hint,
  value,
  onChange,
  className,
}: {
  label: string;
  hint?: string;
  value: string;
  onChange: (value: string) => void;
  className?: string;
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
  const describedBy = hint ? `${id}-hint` : undefined;
  return (
    <fieldset className={cn("min-w-0 space-y-1.5 border-0 p-0", className)}>
      <legend id={id} className="block p-0 text-ui font-medium text-t2">
        {label}
      </legend>
      <div className="flex gap-2">
        <select
          aria-label={t("month.month")}
          aria-describedby={describedBy}
          className={cn(control, "h-10 w-32 sm:h-9")}
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
          className={cn(control, "h-10 w-24 tabular-nums sm:h-9")}
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
        <p id={`${id}-hint`} className="text-label text-t4">
          {hint}
        </p>
      ) : null}
    </fieldset>
  );
}

export function CheckField({ label, hint, className, ...props }: Omit<FieldProps, "error"> & ComponentProps<"input">) {
  const id = useId();
  return (
    <div className={cn("flex items-start gap-2.5", className)}>
      <input
        id={id}
        type="checkbox"
        aria-describedby={hint ? `${id}-hint` : undefined}
        className="mt-0.5 size-4 shrink-0 accent-(--accent)"
        {...props}
      />
      <div className="min-w-0 space-y-0.5">
        <label htmlFor={id} className="block text-ui text-t1 [overflow-wrap:anywhere]">
          {label}
        </label>
        {hint ? (
          <p id={`${id}-hint`} className="text-label text-t4">
            {hint}
          </p>
        ) : null}
      </div>
    </div>
  );
}

// FormError says what went wrong with the whole form, where the user is looking; it is announced when it appears.
export function FormError({ children }: { children: ReactNode }) {
  return (
    <div role="alert" className="flex items-start gap-2 rounded-btn bg-bad-soft px-3 py-2 text-ui">
      <Icon name="alert" className="mt-0.5 shrink-0 text-bad" />
      <div className="min-w-0 flex-1 space-y-0.5 text-t1">{children}</div>
    </div>
  );
}
