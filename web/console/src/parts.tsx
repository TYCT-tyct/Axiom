import { Badge } from "@axiom/ui";
import { useTranslation } from "react-i18next";

// A key's text is never shown again after it is made; the list shows where it starts and ends.
export const maskedKey = (key: { prefix: string; last4: string }) => `${key.prefix}…${key.last4}`;

// A time in a datetime-local field is the viewer's own clock; the API takes an instant.
export function toLocalInput(instant: string | undefined) {
  const date = new Date(instant ?? Number.NaN);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

export function toInstant(local: string | undefined) {
  const date = new Date(local ?? Number.NaN);
  return local && !Number.isNaN(date.getTime()) ? date.toISOString() : undefined;
}

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
