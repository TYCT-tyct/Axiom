import i18next from "i18next";

// Register both languages before returning the page, including for a later language switch.
export async function loadTranslatedPage<T extends { messages: { zh: object; en: object } }>(
  page: Promise<T>,
): Promise<T> {
  const loaded = await page;
  for (const language of ["zh", "en"] as const) {
    i18next.addResourceBundle(language, "admin", loaded.messages[language], true);
  }
  return loaded;
}
