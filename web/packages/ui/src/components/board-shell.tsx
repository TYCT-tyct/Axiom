import { type ReactNode, useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";
import { useBrandStyle } from "../brand";
import { chooseLanguage, type Language, languageNames, languages, locales } from "../i18n";
import { chooseTheme, currentTheme, subscribeTheme, themes } from "../preferences";
import styles from "./board-shell.module.css";
import { ForbiddenHint } from "./states";

// BoardShell frames a shell a design board draws (#747) as a page of the app: a skip link to #main, the app's notices
// floating above it, and the rules the board's fixed frame lacks (board-shell.module.css). forbiddenHint and brandColor
// are AppShell's.
export function BoardShell({
  banner,
  forbiddenHint,
  brandColor,
  children,
}: {
  banner?: ReactNode;
  forbiddenHint?: string;
  brandColor?: string;
  children: ReactNode;
}) {
  const { t } = useTranslation("common");
  useBrandStyle(brandColor);
  return (
    <div className={styles.root}>
      <a href="#main" className={styles.skip}>
        {t("shell.skipToContent")}
      </a>
      {banner && <div className={styles.banner}>{banner}</div>}
      <ForbiddenHint value={forbiddenHint}>{children}</ForbiddenHint>
    </div>
  );
}

const darkScheme = () => matchMedia("(prefers-color-scheme: dark)");
const subscribeScheme = (listener: () => void) => {
  const scheme = darkScheme();
  scheme.addEventListener("change", listener);
  return () => scheme.removeEventListener("change", listener);
};

// usePreferences gives a board's switches the page's own theme and language, so the shell shows what the page has:
// dark is the scheme in effect, the system's when the theme follows it; other is the language a switch changes to.
export function usePreferences() {
  const { i18n } = useTranslation();
  const theme = useSyncExternalStore(subscribeTheme, currentTheme);
  const systemDark = useSyncExternalStore(subscribeScheme, () => darkScheme().matches);
  const language = i18n.language as Language;
  const other = languages.find((l) => l !== language) as Language;
  return {
    theme,
    themes,
    chooseTheme,
    dark: theme === "dark" || (theme === "system" && systemDark),
    locale: locales[language],
    other: { name: languageNames[other], locale: locales[other], choose: () => chooseLanguage(other) },
  };
}
