import { useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";
import { chooseLanguage, type Language, languageNames, languages } from "../i18n";
import { chooseTheme, currentTheme, subscribeTheme, type Theme } from "../preferences";
import { Segmented } from "./segmented";

const themeIcons = { system: "monitor", light: "sun", dark: "moon" } as const;

// PreferenceSwitches lets anyone, signed in or not, pick the colour scheme and the language; both are kept in this
// browser. An account's own settings come with the appearance settings page.
export function PreferenceSwitches({ className }: { className?: string }) {
  const { t, i18n } = useTranslation("common");
  const theme = useSyncExternalStore(subscribeTheme, currentTheme);
  return (
    <div className={className}>
      <Segmented
        label={t("preferences.theme")}
        value={theme}
        options={(Object.keys(themeIcons) as Theme[]).map((value) => ({
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
