import { useState } from "react";
import { ActionButton } from "./action-button";
import { Tag } from "./tag";
import { TextBox } from "./text-box";

// SecretField is a write-only setting in its row (the secret setting and adA_secret in admin_set.js): what is saved is
// never shown, only whether there is one, with a button to replace it. Replacing opens a box for the new secret, which
// closes again when it is left empty. value is the new secret typed so far, empty for none.
export function SecretField({
  label,
  isSet,
  value,
  onChange,
  setLabel,
  unsetLabel,
  replaceLabel,
  placeholder,
  disabled = false,
}: {
  label: string;
  isSet: boolean;
  value: string;
  onChange: (value: string) => void;
  setLabel: string;
  unsetLabel: string;
  replaceLabel: string;
  placeholder: string;
  disabled?: boolean;
}) {
  const [replacing, setReplacing] = useState(false);
  if (replacing || value !== "") {
    return (
      <TextBox
        label={label}
        value={value}
        onChange={onChange}
        type="password"
        mono
        width={240}
        placeholder={placeholder}
        disabled={disabled}
        focused={replacing}
        onBlur={() => value === "" && setReplacing(false)}
      />
    );
  }
  return (
    <>
      <Tag tone={isSet ? "ok" : "neutral"} dot>
        {isSet ? setLabel : unsetLabel}
      </Tag>
      <ActionButton size="sm" disabled={disabled} onClick={() => setReplacing(true)}>
        {replaceLabel}
      </ActionButton>
    </>
  );
}
