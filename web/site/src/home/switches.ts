import type { Prefs } from "@axiom/fx/prefs";
import type { Sea } from "@axiom/fx/sea";
import { chooseLanguage, chooseTheme, currentTheme, writeStored } from "@axiom/ui";
import i18next from "i18next";
import { type RefObject, useEffect } from "react";
import { systemScheme } from "./scheme";

const loadPrefs = () => import("@axiom/fx/prefs");

const flip = () => {
  const theme = currentTheme();
  const to = (theme === "system" ? systemScheme() : theme) === "dark" ? "light" : "dark";
  chooseTheme(to === systemScheme() ? "system" : to);
};

// useSwitches loads the language and theme switches' engine (packages/fx prefs) the first time either button of the
// frame root points at is pressed, and hands it that press. From then on the engine listens on them itself. It keeps
// the choices under its own keys, so it is handed the page's own first, and every choice it lands is kept as the
// page's. Without its engine the page still switches: the theme at once, the language by opening the page in it.
export function useSwitches(root: RefObject<HTMLElement | null>, sea: RefObject<Sea | null>) {
  useEffect(() => {
    const frame = root.current;
    if (!frame) return;
    let live = true;
    let prefs: Prefs | undefined;
    let loading: Promise<Prefs> | undefined;
    const start = () =>
      (loading ??= loadPrefs().then(({ startPrefs }) => {
        writeStored("ax-lang", i18next.language === "en" ? "en" : "zh-CN");
        writeStored("ax-theme", currentTheme());
        prefs = startPrefs(frame, sea.current?.engine, {
          system: systemScheme(),
          onTheme: (to) => chooseTheme(to === systemScheme() ? "system" : to),
          onLang: (code) => chooseLanguage(code === "en" ? "en" : "zh"),
        });
        if (!live) prefs.destroy();
        return prefs;
      }));
    const without = (language: boolean) => {
      loading = undefined;
      if (!language) return flip();
      chooseLanguage(i18next.language === "en" ? "zh" : "en");
      window.location.reload();
    };
    const onClick = (e: MouseEvent) => {
      const control = e.target instanceof Element ? e.target.closest(".bar .lang, .bar .theme") : null;
      if (!control || prefs) return;
      e.preventDefault();
      const language = control.matches(".lang");
      start().then(
        (started) => (language ? started.open() : started.toggleTheme()),
        () => without(language),
      );
    };
    const system = matchMedia("(prefers-color-scheme: dark)");
    const onSystem = () => prefs?.setSystem(systemScheme());
    frame.addEventListener("click", onClick);
    system.addEventListener("change", onSystem);
    return () => {
      live = false;
      frame.removeEventListener("click", onClick);
      system.removeEventListener("change", onSystem);
      prefs?.destroy();
    };
  }, [root, sea]);
}
