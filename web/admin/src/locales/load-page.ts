import i18next from "i18next";

type Bundles = { zh: object; en: object };

// Register both languages before returning the page, including for a later language switch. A page's words are either
// deep-merged into the admin's one catalog (messages), or kept in namespaces of their own (namespaces), which the types
// then know apart: the operations pages' words are the latter, so their keys do not weigh on every page's t().
export async function loadTranslatedPage<T extends { messages?: Bundles; namespaces?: Record<string, Bundles> }>(
  page: Promise<T>,
): Promise<T> {
  const loaded = await page;
  for (const language of ["zh", "en"] as const) {
    if (loaded.messages) {
      i18next.addResourceBundle(language, "admin", loaded.messages[language], true);
    }
    for (const [namespace, bundles] of Object.entries(loaded.namespaces ?? {})) {
      i18next.addResourceBundle(language, namespace, bundles[language], true);
    }
  }
  return loaded;
}
