import { expect, test } from "@playwright/test";
import { open, scrollTo, shown } from "./fx-helpers.ts";

// What the home screens say and do in a single tab, held to what the product guarantees: the questions' promises about
// charges and limits, a configuration a reader copies by hand, the one answer for the models every screen reads, the
// menu sheet to the keyboard, and the site's own name and address in the questions' pictures. The rest of the screens'
// cases are fx-screens.spec.ts.

// The promises are written out here, not taken from the page's messages: what settlement guarantees and no more.
const promises = {
  zh: { estimated: "整笔都是估算", paused: "暂停新的调用", over: "略超上限", not: "封住总损失" },
  en: { estimated: "estimated whole", paused: "takes no new calls", over: "slightly past", not: "cap the total loss" },
} as const;
for (const language of ["zh", "en"] as const)
  test(`the questions promise only what settlement guarantees, ${language}`, async ({ page }) => {
    const said = promises[language];
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.addInitScript((lang) => localStorage.setItem("axiom.language", lang), language);
    await page.setViewportSize({ width: 1440, height: 900 });
    await open(page);
    await scrollTo(page, 5);
    await shown(page, 5);
    const answer = (i: number) => page.locator(".hfm5 .ans").nth(i);
    await expect(answer(3)).toContainText(said.estimated);
    await expect(answer(5)).toContainText(said.paused);
    await expect(answer(6)).toContainText(said.over);
    await expect(answer(6)).not.toContainText(said.not);
  });
