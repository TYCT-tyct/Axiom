import type { Schemas } from "@axiom/api";
import { createInstance } from "i18next";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nextProvider } from "react-i18next";
import { beforeAll, describe, expect, it } from "vitest";
import {
  canSubmitWithLegal,
  LegalConsent,
  type LegalConsentState,
  legalDocumentsForLanguage,
  legalVersions,
} from "./legal";

const i18n = createInstance();

beforeAll(async () => {
  await i18n.init({
    lng: "en",
    defaultNS: "console",
    resources: {
      en: {
        console: {
          legal: {
            terms: "Terms of Service",
            privacy: "Privacy Policy",
            version: "Version {{version}}",
            loading: "Loading the terms…",
            error: "The terms didn’t load.",
            retry: "Retry",
            accept: "I have read and agree.",
          },
        },
      },
    },
  });
});

const document = (
  kind: "terms" | "privacy",
  version: number,
  locale: Schemas["LegalDocumentLocale"] = "en",
): Schemas["LegalDocument"] => ({
  kind,
  locale,
  version,
  body: `Demo text, not legal advice; replace before launch.\n\n====\n\n# ${kind} heading\n\n## Section\n\nBody paragraph.`,
  published_at: "2026-11-04T00:00:00Z",
});

function state(
  documents: Schemas["LegalDocument"][],
  accepted: boolean,
  flags: { pending?: boolean; error?: boolean } = {},
): LegalConsentState {
  return {
    query: { isPending: flags.pending ?? false, isError: flags.error ?? false } as LegalConsentState["query"],
    documents,
    accepted,
    setAccepted: () => {},
    versions: legalVersions(documents),
    ready: !(flags.pending ?? false) && !(flags.error ?? false),
    reload: () => {},
  };
}

describe("legal consent", () => {
  it("resolves the legal keys in the console namespace", () => {
    expect(i18n.t("legal.error")).toBe("The terms didn’t load.");
  });

  it("renders the displayed documents, their versions and the unchecked acceptance", () => {
    const html = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <LegalConsent consent={state([document("terms", 3), document("privacy", 1)], false)} />
      </I18nextProvider>,
    );
    expect(html).toContain("Terms of Service");
    expect(html).toContain("Version 3");
    expect(html).toContain("Privacy Policy");
    expect(html).toContain("Version 1");
    expect(html).toContain("Demo text, not legal advice");
    expect(html).toContain("terms heading");
    expect(html).toContain("<h3");
    expect(html).toContain("<h4");
    expect(html).not.toContain("## terms heading");
    expect(html).not.toContain("====");
    expect(html).toContain("I have read and agree.");
    expect(html).toContain('type="checkbox"');
    expect(html).not.toContain("checked");
  });

  it("renders nothing when the site publishes no documents", () => {
    const html = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <LegalConsent consent={state([], false)} />
      </I18nextProvider>,
    );
    expect(html).toBe("");
  });

  it("shows the read failure with a retry", () => {
    const html = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <LegalConsent consent={state([], false, { error: true })} />
      </I18nextProvider>,
    );
    expect(html).toContain("The terms didn’t load.");
    expect(html).toContain("Retry");
  });

  it("requires acceptance only when there are documents, and only after they loaded", () => {
    const terms = [document("terms", 1)];
    expect(canSubmitWithLegal({ ready: false, documents: [], accepted: false })).toBe(false);
    expect(canSubmitWithLegal({ ready: true, documents: [], accepted: false })).toBe(true);
    expect(canSubmitWithLegal({ ready: true, documents: terms, accepted: false })).toBe(false);
    expect(canSubmitWithLegal({ ready: true, documents: terms, accepted: true })).toBe(true);
  });

  it("keeps an old node's missing locale out of the acceptance", () => {
    const legacy: Schemas["LegalDocument"] = {
      kind: "terms",
      version: 1,
      body: "Legacy terms.",
      published_at: "2026-11-04T00:00:00Z",
    };
    expect(legalDocumentsForLanguage([legacy], "en")).toEqual([legacy]);
    expect(legalDocumentsForLanguage([legacy], "zh")).toEqual([legacy]);
    expect(legalVersions([legacy])).toEqual([{ kind: "terms", version: 1 }]);
  });

  it("selects the newest shared version before language, falls back per document, and orders terms first", () => {
    const privacy = document("privacy", 1, "en");
    const zhTerms = document("terms", 7, "zh");
    const olderTerms = document("terms", 4, "en");
    expect(legalDocumentsForLanguage([privacy, olderTerms, zhTerms], "en-US")).toEqual([zhTerms, privacy]);
    expect(legalDocumentsForLanguage([privacy, zhTerms], "en")).toEqual([zhTerms, privacy]);
    expect(legalDocumentsForLanguage([privacy, olderTerms], "zh")).toEqual([olderTerms, privacy]);

    const zhCurrent = document("terms", 2, "zh");
    const enCurrent = document("terms", 2, "en");
    expect(legalDocumentsForLanguage([zhCurrent, enCurrent], "en-US")).toEqual([enCurrent]);
    expect(legalDocumentsForLanguage([zhCurrent, enCurrent], "zh-CN")).toEqual([zhCurrent]);
    expect(legalDocumentsForLanguage([], "en")).toEqual([]);
  });

  it("sends exactly the versions that were shown, including a locale only when the response had one", () => {
    expect(legalVersions([document("terms", 4), document("privacy", 2, "zh")])).toEqual([
      { kind: "terms", locale: "en", version: 4 },
      { kind: "privacy", locale: "zh", version: 2 },
    ]);
    const legacy: Schemas["LegalDocument"] = {
      kind: "terms",
      version: 3,
      body: "Legacy terms.",
      published_at: "2026-11-04T00:00:00Z",
    };
    expect(legalVersions([legacy, document("privacy", 2, "en")])).toEqual([
      { kind: "terms", version: 3 },
      { kind: "privacy", locale: "en", version: 2 },
    ]);
    expect(legalVersions([])).toBeUndefined();
  });
});
