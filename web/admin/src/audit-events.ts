import { useTranslation } from "react-i18next";

// What an audit entry's action is called. The words cover every action the backend writes (locales/zh/audit-events.ts);
// one a newer node writes that they do not name reads as a generic label, with its own name beside it for whoever has
// to look it up.
export type EventLabel = { text: string; raw?: string };

export function eventLabel(words: Record<string, string>, unknown: string, action: string | undefined): EventLabel {
  if (!action) return { text: "—" };
  return Object.hasOwn(words, action) ? { text: words[action] as string } : { text: unknown, raw: action };
}

export function useEventLabel() {
  const { t } = useTranslation();
  const words = t("auditEvents", { returnObjects: true }) as Record<string, string>;
  const unknown = t("audit.unknownEvent");
  return (action: string | undefined) => eventLabel(words, unknown, action);
}
