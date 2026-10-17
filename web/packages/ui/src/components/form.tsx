import { type ComponentProps, type ReactNode, useId } from "react";
import { cn } from "../cn";
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
