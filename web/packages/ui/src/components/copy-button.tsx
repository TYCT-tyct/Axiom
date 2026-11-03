import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "./button";
import { Icon } from "./icon";

// CopyButton puts text on the clipboard and says so. The clipboard needs a secure page and a permission, so a refusal
// is said too, and the text stays on screen to select by hand. `label` names what it copies, for a page whose buttons
// would otherwise all read the same. `compact` is the icon-sized form for a table cell, where the words would take the
// room the value needs; the outcome is still announced.
export function CopyButton({ text, label, compact = false }: { text: string; label?: string; compact?: boolean }) {
  const { t } = useTranslation("common");
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
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
  if (compact) {
    return (
      <>
        <Button
          variant="ghost"
          size="icon"
          className="size-6 text-t4 hover:text-t1"
          aria-label={label ?? t("copy.copy")}
          title={label ?? t("copy.copy")}
          onClick={copy}
        >
          <Icon name={state === "copied" ? "check" : "copy"} width="14" height="14" />
        </Button>
        <span role="status" className="sr-only">
          {state === "idle" ? "" : t(`copy.${state}`)}
        </span>
      </>
    );
  }
  return (
    <>
      <Button variant="secondary" onClick={copy}>
        <Icon name={state === "copied" ? "check" : "copy"} />
        {state === "copied" ? t("copy.copied") : (label ?? t("copy.copy"))}
      </Button>
      <span role="status" className="text-label text-t3">
        {state === "idle" ? "" : t(`copy.${state}`)}
      </span>
    </>
  );
}
