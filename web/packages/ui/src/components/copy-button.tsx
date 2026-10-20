import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "./button";
import { Icon } from "./icon";

// CopyButton puts text on the clipboard and says so. The clipboard needs a secure page and a permission, so a refusal
// is said too, and the text stays on screen to select by hand. `label` names what it copies, for a page whose buttons
// would otherwise all read the same.
export function CopyButton({ text, label }: { text: string; label?: string }) {
  const { t } = useTranslation("common");
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  return (
    <>
      <Button
        variant="secondary"
        onClick={() => {
          navigator.clipboard.writeText(text).then(
            () => setState("copied"),
            () => setState("failed"),
          );
        }}
      >
        <Icon name={state === "copied" ? "check" : "copy"} />
        {state === "idle" ? (label ?? t("copy.copy")) : t(`copy.${state}`)}
      </Button>
      <span role="status" className="text-label text-t3">
        {state === "idle" ? "" : t(`copy.${state}`)}
      </span>
    </>
  );
}
