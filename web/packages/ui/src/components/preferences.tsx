import { useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";
import { classes } from "../classes";
import { chooseLanguage, type Language, languageNames, languages } from "../i18n";
import { chooseTheme, currentTheme, subscribeTheme, type Theme, themes } from "../preferences";
import type { IconName } from "./icon";
import s from "./preferences.module.css";
import { Segmented } from "./segmented";

const themeIcons: Record<Theme, IconName> = { system: "monitor", light: "sun", dark: "moon" };

// PreferenceSwitches lets anyone, signed in or not, pick the colour scheme and the language; both are kept in this
// browser. An account's own settings come with the appearance settings page.
// className is the one a page's frame gives it to place it (auth-page.tsx, the site shell).
export function PreferenceSwitches({ className }: { className?: string }) {
  const { t, i18n } = useTranslation("common");
  const theme = useSyncExternalStore(subscribeTheme, currentTheme);
  return (
    <div className={classes(s.root, className)}>
      <Segmented
        label={t("preferences.theme")}
        value={theme}
        options={themes.map((value) => ({
          value,
          label: t(`preferences.themes.${value}`),
          icon: themeIcons[value],
        }))}
        onChange={chooseTheme}
      />
      <Segmented
        label={t("preferences.language")}
        value={i18n.language as Language}
        options={languages.map((value) => ({ value, label: languageNames[value] }))}
        onChange={chooseLanguage}
      />
    </div>
  );
}
