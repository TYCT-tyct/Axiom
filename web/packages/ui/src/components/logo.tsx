import { useTranslation } from "react-i18next";
import { cn } from "../cn";

// The mark is an A whose crossbar is a dot, the one key; it takes the theme's text and page colours.
export function Logo({ className }: { className?: string }) {
  const { t } = useTranslation("common");
  return (
    <span
      className={cn("inline-flex items-center gap-2.5 text-[15px] font-semibold tracking-[-0.01em] text-t1", className)}
    >
      <svg viewBox="0 0 20 20" width="20" height="20" aria-hidden="true">
        <rect width="20" height="20" rx="5.5" fill="var(--t1)" />
        <path
          d="M5.75 14.6 9.05 6.35q.95-1.75 1.9 0l3.3 8.25"
          fill="none"
          stroke="var(--bg)"
          strokeWidth="1.7"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
        <circle cx="10" cy="12.3" r="1.3" fill="var(--bg)" />
      </svg>
      {t("brand")}
    </span>
  );
}
