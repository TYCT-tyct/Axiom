import { useId, useRef, useState } from "react";
import CheckboxRegion from "./AxApp/regions/AdminCheckbox.tsx";
import FeedbackRegion from "./AxApp/regions/AdminFieldFeedback.tsx";
import SelectRegion from "./AxApp/regions/AdminSelectField.tsx";
import InputRegion from "./AxApp/regions/PartTextField.tsx";
import { ActionButton } from "./ds-admin-actions.tsx";

type Field = { label: string; name: string; hint?: string; error?: string; disabled?: boolean };
type ValueField = Field & { value: string; onChange: (value: string) => void };

function useFeedback({ hint, error }: Pick<Field, "hint" | "error">) {
  const feedbackId = useId();
  const text = error ?? hint;
  return {
    text: text ?? "",
    values: { feedbackId, feedbackClass: error ? "nkerr" : "nkhint", hideFeedback: !text },
    describedBy: text ? feedbackId : undefined,
  };
}

function markControl(control: HTMLElement, { disabled, error }: Pick<Field, "disabled" | "error">) {
  control.toggleAttribute("disabled", disabled ?? false);
  if (error) control.setAttribute("aria-invalid", "true");
  else control.removeAttribute("aria-invalid");
}

export function SelectField({
  options,
  label,
  name,
  value,
  onChange,
  ...field
}: ValueField & { options: readonly { value: string; label: string; disabled?: boolean }[] }) {
  const id = useId();
  const { text, values, describedBy } = useFeedback(field);
  return (
    <SelectRegion
      t={String}
      text={{ label, feedback: text }}
      values={{
        ...values,
        id,
        name,
        initial: value,
        describedBy,
        disabled: field.disabled,
        invalidClass: field.error ? "bad" : "",
        control: (node) => {
          if (!(node instanceof HTMLSelectElement)) return;
          node.value = value;
          markControl(node, field);
        },
        input: (event) => {
          const control = event.currentTarget as HTMLSelectElement;
          const chosen = control.value;
          control.value = value;
          onChange(chosen);
        },
      }}
      options={options.map((option) => (
        <option key={option.value} value={option.value} disabled={option.disabled}>
          {option.label}
        </option>
      ))}
    />
  );
}

export function AdminInput({
  label,
  name,
  value,
  onChange,
  placeholder,
  maxLength,
  inputType = "text",
  list,
  ...field
}: ValueField & { inputType?: "text" | "datetime-local"; placeholder?: string; maxLength?: number; list?: string }) {
  const id = useId();
  const { text, values, describedBy } = useFeedback(field);
  return (
    <InputRegion
      t={String}
      text={{ label, message: text }}
      values={{
        id,
        name,
        initial: value,
        placeholder,
        inputType,
        autocomplete: "off",
        describedBy,
        descriptionId: values.feedbackId,
        messageClass: values.feedbackClass,
        hideMessage: values.hideFeedback,
        invalidClass: field.error ? "bad" : "",
        control: (node) => {
          if (!(node instanceof HTMLInputElement)) return;
          if (node.value !== value) node.value = value;
          if (list) node.setAttribute("list", list);
          else node.removeAttribute("list");
          if (maxLength !== undefined) node.maxLength = maxLength;
          markControl(node, field);
        },
        input: (event) => onChange((event.currentTarget as HTMLInputElement).value),
      }}
    />
  );
}

export function SuggestionField({
  suggestions,
  ...field
}: ValueField & { suggestions: readonly string[]; placeholder?: string; maxLength?: number }) {
  const list = useId();
  return (
    <>
      <AdminInput {...field} list={list} />
      <datalist id={list}>
        {suggestions.map((suggestion) => (
          <option key={suggestion} value={suggestion} />
        ))}
      </datalist>
    </>
  );
}

export function CheckboxField({
  label,
  name,
  value = "on",
  checked,
  onChange,
  disabled,
  ...field
}: Field & { value?: string; checked: boolean; onChange: (checked: boolean) => void }) {
  const id = useId();
  const { text, values, describedBy } = useFeedback(field);
  return (
    <div className="nkf">
      <CheckboxRegion
        t={String}
        text={{ label }}
        values={{
          id,
          label,
          checked,
          describedBy,
          disabled,
          checkedClass: `${checked ? "on" : ""} ${disabled ? "pplock" : ""}`,
          toggle: () => onChange(!checked),
        }}
      />
      <input type="checkbox" name={name} value={value} checked={checked} disabled={disabled} hidden readOnly />
      <FeedbackRegion t={String} text={{ feedback: text }} values={values} />
    </div>
  );
}

export function FileField({
  label,
  name,
  accept,
  disabled,
  onChange,
  ...field
}: Field & { accept?: string; onChange: (file: File | undefined) => void }) {
  const control = useRef<HTMLInputElement>(null);
  const [selected, setSelected] = useState("");
  const { text, values, describedBy } = useFeedback(field);
  return (
    <div className="nkf">
      <ActionButton describedBy={describedBy} disabled={disabled} onClick={() => control.current?.click()}>
        {label}
      </ActionButton>
      <input
        ref={control}
        type="file"
        name={name}
        accept={accept}
        disabled={disabled}
        hidden
        onChange={(event) => {
          const file = event.currentTarget.files?.[0];
          setSelected(file?.name ?? "");
          onChange(file);
        }}
      />
      <div role="status">{selected}</div>
      <FeedbackRegion t={String} text={{ feedback: text }} values={values} />
    </div>
  );
}
