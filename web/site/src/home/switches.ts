import type { Prefs } from "@axiom/fx/prefs";
import type { Sea } from "@axiom/fx/sea";
import { chooseLanguage, chooseTheme, currentTheme, writeStored } from "@axiom/ui";
import i18next from "i18next";
import { type RefObject, useEffect } from "react";
import { systemScheme } from "./scheme";

const loadPrefs = () => import("@axiom/fx/prefs");

const effective = () => {
  const theme = currentTheme();
  return theme === "system" ? systemScheme() : theme;
};

const flip = () => {
  const to = effective() === "dark" ? "light" : "dark";
  chooseTheme(to === systemScheme() ? "system" : to);
};

// The footer's own choices (the sea bed's), each marked with what it picks: the language, or the appearance.
const footerPick = ".under .ft [data-pick]";
type Pick = "zh" | "en" | "light" | "dark";

// A press on one of the switches: the header's globe and moon, or a choice in the footer.
type Press = { control: Element; footer?: HTMLElement; pick?: Pick };
function pressOf(e: MouseEvent): Press | null {
  const target = e.target instanceof Element ? e.target : null;
  const footer = target?.closest<HTMLElement>(footerPick) ?? undefined;
  const control = footer ?? target?.closest(".bar .lang, .bar .theme");
  return control ? { control, footer, pick: footer?.dataset.pick as Pick | undefined } : null;
}
const isLanguage = ({ control, pick }: Press) => (pick ? pick === "zh" || pick === "en" : control.matches(".lang"));

// What a press asks of the engine: the header's buttons open the panel or flip the theme; a footer choice names the
// language or the appearance it picks, which may be the one already shown.
function hand(started: Prefs, press: Press) {
  const { pick, footer } = press;
  if (!pick) return isLanguage(press) ? started.open() : started.toggleTheme();
  if (isLanguage(press)) return started.choose(pick === "en" ? "en" : "zh-CN", footer);
  if (pick !== effective()) started.toggleTheme();
}

// The same press without the engine (its module failed to load): the theme changes at once, the language by opening
// the page in it.
function without(press: Press) {
  if (!isLanguage(press)) return press.pick === effective() || flip();
  const now = i18next.language === "en" ? "en" : "zh";
  const to = press.pick === "zh" || press.pick === "en" ? press.pick : now === "en" ? "zh" : "en";
  if (to === now) return;
  chooseLanguage(to);
  window.location.reload();
}

// useSwitches loads the language and theme switches' engine (packages/fx prefs) the first time either button of the
// frame root points at is pressed, or a choice in the footer, and hands it that press. From then on the engine listens
// on the buttons itself; the footer's choices are the page's own, handed to it each time. It keeps
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
        sea.current?.settle?.();
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
    const onClick = (e: MouseEvent) => {
      const press = pressOf(e);
      // once the engine is there it listens on the header's buttons itself; the footer's choices are the page's
      if (!press) return;
      sea.current?.settle?.();
      if (prefs && !press.footer) return;
      e.preventDefault();
      start().then(
        (started) => hand(started, press),
        () => {
          loading = undefined;
          without(press);
        },
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
