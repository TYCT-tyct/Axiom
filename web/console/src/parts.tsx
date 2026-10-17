import { Badge } from "@axiom/ui";
import { useTranslation } from "react-i18next";

// A key's text is never shown again after it is made; the list shows where it starts and ends.
export const maskedKey = (key: { prefix: string; last4: string }) => `${key.prefix}…${key.last4}`;

// How a call ended, in words with an icon. In a crowded row (compact) a phone shows the icon alone and leaves the words
// to a screen reader; the two icons differ in shape, so the colour is still not the only signal.
export function CallStatus({ errorKind, compact = false }: { errorKind: string; compact?: boolean }) {
  const { t } = useTranslation();
  const failed = errorKind !== "";
  return (
    <Badge tone={failed ? "bad" : "ok"} icon={failed ? "alert" : "check"}>
      <span className={compact ? "sr-only sm:not-sr-only" : undefined}>
        {failed ? t("calls.failed") : t("calls.ok")}
      </span>
    </Badge>
  );
}
