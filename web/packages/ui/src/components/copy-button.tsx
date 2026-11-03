import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "./button";
import { Icon } from "./icon";

type CopyState = "idle" | "copied" | "failed";

const icons = { idle: "copy", copied: "check", failed: "alert" } as const;

// The compact form is an icon inside a table cell. A failure adds a visible line beside it, because the words are the
// only next step a sighted user gets; a success and an idle state stay silent visually and are only announced.
function CompactCopyButton({
  label,
  state,
  message,
  onCopy,
}: {
  label: string;
  state: CopyState;
  message: string;
  onCopy: () => void;
}) {
  const failed = state === "failed";
  return (
    <>
      <Button
        variant="ghost"
        size="icon"
        className={`size-6 ${failed ? "text-bad" : "text-t4 hover:text-t1"}`}
        aria-label={label}
        title={label}
        onClick={onCopy}
      >
        <Icon name={icons[state]} width="14" height="14" />
      </Button>
      <span role="status" className={failed ? "min-w-0 text-label text-bad [overflow-wrap:anywhere]" : "sr-only"}>
        {message}
      </span>
    </>
  );
}

// CopyButton puts text on the clipboard and says so. The clipboard needs a secure page and a permission, so a refusal
// is said too, and the text stays on screen to select by hand. `label` names what it copies, for a page whose buttons
// would otherwise all read the same. `compact` is the icon-sized form for a table cell, where the words would take the
// room the value needs; the outcome is still announced and a failure is visible too.
export function CopyButton({ text, label, compact = false }: { text: string; label?: string; compact?: boolean }) {
  const { t } = useTranslation("common");
  const [state, setState] = useState<CopyState>("idle");
  const copy = () => {
    if (!navigator.clipboard) {
      setState("failed");
      return;
    }
    navigator.clipboard.writeText(text).then(
      () => setState("copied"),
      () => setState("failed"),
    );
  };
  const name = label ?? t("copy.copy");
  const messages = { idle: "", copied: t("copy.copied"), failed: t("copy.failed") };
  if (compact) {
    return <CompactCopyButton label={name} state={state} message={messages[state]} onCopy={copy} />;
  }
  return (
    <>
      <Button variant="secondary" onClick={copy}>
        <Icon name={icons[state]} />
        {state === "copied" ? t("copy.copied") : name}
      </Button>
      <span role="status" className="text-label text-t3">
        {messages[state]}
      </span>
    </>
  );
}
