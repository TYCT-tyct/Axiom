import { useState } from "react";
import { useTranslation } from "react-i18next";
import { classes } from "../classes";
import s from "./logo.module.css";

// The mark is an A whose crossbar is a dot, the one key; it takes the theme's text and page colours. A site brings its
// own name and logo (docs/domains/accounts.md「站点」); a logo that does not load shows the mark.
// className is the one the auth frame still puts on it (packages/app/src/auth/frame.tsx) and goes with that file's move.
export function Logo({ className, name, logoUrl }: { className?: string; name?: string; logoUrl?: string }) {
  const { t } = useTranslation("common");
  // The URL that failed, not a boolean: a new logo at a new URL is tried, not hidden behind the old failure.
  const [failed, setFailed] = useState<string | null>(null);
  return (
    <span className={classes(s.root, className)}>
      {logoUrl && failed !== logoUrl ? (
        <img src={logoUrl} alt="" width="20" height="20" onError={() => setFailed(logoUrl)} className={s.image} />
      ) : (
        <svg viewBox="0 0 20 20" width="20" height="20" aria-hidden="true" className={s.mark}>
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
      )}
      <span className={s.name}>{name ?? t("brand")}</span>
    </span>
  );
}
