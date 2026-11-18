import { ApiError, consoleApi, type Schemas, unwrap, unwrapEmpty } from "@axiom/api";
import { accountKey, useErrors, useSessionBound, useShowRecoveryCodes, useStartSession } from "@axiom/app";
import type DesktopProfile from "@axiom/boards/AxApp/regions/Profile";
import type DesktopSecurity from "@axiom/boards/AxApp/regions/Security";
import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type SyntheticEvent, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { type ThemeChoice, useBoardInputs } from "./account-board";

export type DisplayNameState = { available: boolean; name: string };
export type BillingOption = "site" | Schemas["BillingPreferenceRequest"]["preference"];
export type EmailConfirmStatus =
  | { state: "idle" }
  | { state: "pending" }
  | { state: "confirmed" }
  | { state: "error"; error: unknown };

export const billingOptions: readonly BillingOption[] = [
  "site",
  "subscription_first",
  "wallet_first",
  "subscription_only",
  "wallet_only",
];

// Read the personal account's display name; older backend nodes (404 or absent `name`) degrade cleanly.
export const displayNameQuery = queryOptions({
  queryKey: [accountKey, "profile", "name"],
  queryFn: async ({ signal }): Promise<DisplayNameState> => {
    try {
      const data = await unwrap(consoleApi.GET("/api/console/v1/profile/name", { signal }));
      return data.name === undefined ? { available: false, name: "" } : { available: true, name: data.name };
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) {
        return { available: false, name: "" };
      }
      throw error;
    }
  },
});

export function validateDisplayName(raw: string): "empty" | "tooLong" | null {
  const count = Array.from(raw.trim()).length;
  if (count === 0) return "empty";
  if (count > 100) return "tooLong";
  return null;
}

export function validateEmailChange(
  rawEmail: string,
  currentEmail: string,
  password: string,
): "invalid" | "unchanged" | "passwordRequired" | null {
  const email = rawEmail.trim();
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) return "invalid";
  if (email.toLowerCase() === currentEmail.trim().toLowerCase()) return "unchanged";
  if (password === "") return "passwordRequired";
  return null;
}

export function readEmailChangeToken(hash: string): string | null {
  if (!hash.startsWith("#")) return null;
  const token = new URLSearchParams(hash.slice(1)).get("email-change-token")?.trim() ?? "";
  return token || null;
}

export function stripEmailChangeHash(loc: Pick<Location, "pathname" | "search" | "hash">): string {
  const params = new URLSearchParams(loc.hash.startsWith("#") ? loc.hash.slice(1) : loc.hash);
  params.delete("email-change-token");
  const rest = params.toString();
  return `${loc.pathname}${loc.search}${rest ? `#${rest}` : ""}`;
}

export function initialGlyph(name: string, email: string): { glyph: string; title: string } {
  const source = (name.trim() || email.trim() || "?").trim();
  return {
    glyph: Array.from(source)[0]?.toUpperCase() ?? "?",
    title: name.trim() || email.trim(),
  };
}

type ProfileRows = Parameters<typeof DesktopProfile>[0]["rows"]["rows0"];
type SessionCardRows = Parameters<typeof DesktopSecurity>[0]["rows"]["sessions0"];

export function buildProfileRows(opts: {
  t: TFunction;
  name: string;
  email: string;
  nameAvailable: boolean;
  nameFeedback: string | null;
  nameKeySuffix: string;
}): ProfileRows {
  const { glyph, title } = initialGlyph(opts.name, opts.email);
  const nameHint = opts.nameAvailable
    ? (opts.nameFeedback ?? opts.t("settings.profile.nameHint"))
    : opts.t("settings.profile.nameUnavailable");
  return [
    {
      key: "avatar",
      kind: "0",
      f0: "qrow",
      f1: "qrl",
      f2: opts.t("settings.profile.avatarLabel"),
      f3: "qrc",
      f4: "qav lg",
      f5: "--p: var(--c0);",
      f6: title,
      f7: glyph,
    },
    {
      key: `name:${opts.nameKeySuffix}`,
      kind: "2",
      f0: "qrow",
      f1: "qrl",
      f2: opts.t("settings.profile.name"),
      f3: nameHint,
      f4: "qrc",
      f5: "qin",
      f6: "name",
      f7: opts.name,
      f8: opts.t("settings.profile.name"),
    },
    {
      key: `email:${opts.email}`,
      kind: "2",
      f0: "qrow",
      f1: "qrl",
      f2: opts.t("settings.profile.email"),
      f3: opts.t("settings.profile.emailHint"),
      f4: "qrc",
      f5: "qin",
      f6: "email",
      f7: opts.email,
      f8: opts.t("settings.profile.email"),
    },
  ];
}

export function buildProfileActionRows(opts: {
  t: TFunction;
  nameAvailable: boolean;
  nameFeedback: string | null;
  emailSentFeedback: string | null;
}): SessionCardRows {
  const rows: Parameters<typeof DesktopSecurity>[0]["rows"]["sessions0"][number][] = [];
  if (opts.nameAvailable) {
    rows.push({
      key: "save-name",
      kind: "1",
      f0: "qses",
      f1: opts.t("settings.profile.nameSaveAction"),
      f2: opts.nameFeedback ?? opts.t("settings.profile.actionsNameHint"),
      f3: "button",
      f4: "btn sec sm ",
      f5: opts.t("settings.save"),
    });
  }
  rows.push({
    key: "change-email",
    kind: "1",
    f0: "qses",
    f1: opts.t("settings.profile.emailChangeAction"),
    f2: opts.emailSentFeedback ?? opts.t("settings.profile.actionsEmailHint"),
    f3: "button",
    f4: "btn sec sm ",
    f5: opts.t("settings.profile.emailChangeAction"),
  });
  return rows;
}

export function buildThemeRows(t: TFunction, active: ThemeChoice): SessionCardRows {
  return (["system", "light", "dark"] as const).map((theme) => {
    const label = t(`settings.appearance.${theme}`);
    const hint = t(`settings.appearance.${theme}Hint`);
    return theme === active
      ? { key: theme, kind: "0", f0: "qses", f1: label, f2: hint, f3: "qtag ok", f4: t("settings.appearance.current") }
      : {
          key: theme,
          kind: "1",
          f0: "qses",
          f1: label,
          f2: hint,
          f3: "button",
          f4: "btn sec sm ",
          f5: t("settings.appearance.select"),
        };
  });
}

export function buildLanguageRows(t: TFunction, english: boolean): SessionCardRows {
  const active = english ? "en" : "zh";
  return (["zh", "en"] as const).map((lang) => {
    const label = t(`settings.appearance.${lang}`);
    const hint = t(`settings.appearance.${lang}Hint`);
    return lang === active
      ? { key: lang, kind: "0", f0: "qses", f1: label, f2: hint, f3: "qtag ok", f4: t("settings.appearance.current") }
      : {
          key: lang,
          kind: "1",
          f0: "qses",
          f1: label,
          f2: hint,
          f3: "button",
          f4: "btn sec sm ",
          f5: t("settings.appearance.select"),
        };
  });
}

export function buildBillingRows(t: TFunction, data: Schemas["BillingPreference"]): SessionCardRows {
  const active: BillingOption = data.source === "account" ? data.preference : "site";
  return billingOptions.map((option) => {
    const label =
      option === "site"
        ? t("settings.billing.site", { value: t(`settings.billing.values.${data.site_default}`) })
        : t(`settings.billing.values.${option}`);
    const description =
      option === "site"
        ? t("settings.billing.hint", { fallback: t(`settings.billing.values.${data.site_default}`) })
        : t(`settings.billing.descriptions.${option}`);
    return option === active
      ? {
          key: option,
          kind: "0",
          f0: "qses",
          f1: label,
          f2: description,
          f3: "qtag ok",
          f4: t("settings.billing.active"),
        }
      : {
          key: option,
          kind: "1",
          f0: "qses",
          f1: label,
          f2: description,
          f3: "button",
          f4: "btn sec sm ",
          f5: t("settings.billing.choose"),
        };
  });
}

const nameErrorKeys = {
  empty: "settings.profile.nameEmpty",
  tooLong: "settings.profile.nameTooLong",
} as const;

function useDisplayNameMutation(
  setNameDraft: (v: string) => void,
  setNameMessage: (v: string | null) => void,
  onSaved: (v: string) => void,
) {
  const { t } = useTranslation();
  const errors = useErrors();
  const client = useQueryClient();
  return useMutation({
    mutationFn: (trimmed: string) =>
      unwrapEmpty(consoleApi.POST("/api/console/v1/profile/name", { body: { name: trimmed } })),
    onSuccess: (_res, trimmed) => {
      client.setQueryData(displayNameQuery.queryKey, { available: true, name: trimmed });
      setNameDraft(trimmed);
      setNameMessage(t("settings.profile.nameSaved"));
      onSaved(trimmed);
    },
    onError: (err) => setNameMessage(errors.message(err)),
  });
}

function handleProfileKey(
  event: SyntheticEvent,
  handlers: {
    onNameEnter: (val: string) => void;
    onNameEscape: (el: HTMLInputElement) => void;
    onEmailEnter: () => void;
  },
) {
  if (!(event.target instanceof HTMLInputElement)) return;
  const key = (event as unknown as KeyboardEvent).key;
  const k = event.target.dataset.k;
  if (k === "name" && key === "Enter") {
    event.preventDefault();
    handlers.onNameEnter(event.target.value);
  } else if (k === "name" && key === "Escape") {
    handlers.onNameEscape(event.target);
  } else if (k === "email" && key === "Enter") {
    event.preventDefault();
    handlers.onEmailEnter();
  }
}

function useProfileInputSync(
  nameDirty: { current: boolean },
  nameData: DisplayNameState | undefined,
  currentEmail: string,
  setNameDraft: (v: string) => void,
  setEmailDraft: (v: string) => void,
  setValue: (key: string, v: string) => void,
) {
  useEffect(() => {
    if (!nameDirty.current && nameData?.available) {
      setNameDraft(nameData.name);
      setValue("name", nameData.name);
    }
  }, [nameData?.available, nameData?.name, nameDirty, setNameDraft, setValue]);

  useEffect(() => {
    setEmailDraft(currentEmail);
    setValue("email", currentEmail);
  }, [currentEmail, setEmailDraft, setValue]);
}

function buildProfileCallbacks(opts: {
  serverName: string;
  nameDraft: string;
  nameDirty: { current: boolean };
  setNameDraft: (v: string) => void;
  setEmailDraft: (v: string) => void;
  setNameMessage: (v: string | null) => void;
  setValue: (k: string, v: string) => void;
  submitName: (raw: string) => void;
  openEmailModal: () => void;
}) {
  return {
    onInput: (event: SyntheticEvent) => {
      if (!(event.target instanceof HTMLInputElement)) return;
      if (event.target.dataset.k === "name") {
        opts.nameDirty.current = true;
        opts.setNameDraft(event.target.value);
        opts.setNameMessage(null);
      } else if (event.target.dataset.k === "email") opts.setEmailDraft(event.target.value);
    },
    onKeyDown: (event: SyntheticEvent) =>
      handleProfileKey(event, {
        onNameEnter: opts.submitName,
        onNameEscape: (el) => {
          opts.nameDirty.current = false;
          opts.setNameDraft(opts.serverName);
          opts.setValue("name", opts.serverName);
          opts.setNameMessage(null);
          el.blur();
        },
        onEmailEnter: opts.openEmailModal,
      }),
    onActionIndex: (rows: readonly { key: string }[]) => (index: number) => {
      const key = rows[index]?.key;
      if (key === "save-name") opts.submitName(opts.nameDraft || opts.serverName);
      if (key === "change-email") opts.openEmailModal();
    },
  };
}

export function useProfileActions(nameData: DisplayNameState | undefined, currentEmail: string) {
  const { t } = useTranslation();
  const [nameDraft, setNameDraft] = useState("");
  const [emailDraft, setEmailDraft] = useState(currentEmail);
  const [nameMessage, setNameMessage] = useState<string | null>(null);
  const [banner, setBanner] = useState<string | null>(null);
  const [emailSentTo, setEmailSentTo] = useState<string | null>(null);
  const [emailModalOpen, setEmailModalOpen] = useState(false);
  const nameDirty = useRef(false);
  const serverName = nameData?.available ? nameData.name : "";
  const saveName = useDisplayNameMutation(setNameDraft, setNameMessage, (trimmed) => {
    nameDirty.current = false;
    inputs.setValue("name", trimmed);
    setBanner(t("settings.profile.nameSaved"));
  });

  function submitName(raw: string) {
    if (nameData?.available === false || saveName.isPending) return;
    const issue = validateDisplayName(raw);
    if (issue) return setNameMessage(t(nameErrorKeys[issue]));
    const trimmed = raw.trim();
    if (trimmed === serverName) {
      nameDirty.current = false;
      return setNameMessage(null);
    }
    saveName.mutate(trimmed);
  }

  const inputs = useBoardInputs({
    name: { maxLength: 100, onBlur: (el) => nameDirty.current && submitName(el.value) },
    email: { type: "email", autoComplete: "email" },
  });
  useProfileInputSync(nameDirty, nameData, currentEmail, setNameDraft, setEmailDraft, inputs.setValue);

  return {
    inputs,
    nameMessage,
    nameSaving: saveName.isPending,
    banner,
    clearBanner: () => setBanner(null),
    emailDraft,
    emailSentTo,
    emailModalOpen,
    setEmailModalOpen,
    ...buildProfileCallbacks({
      serverName,
      nameDraft,
      nameDirty,
      setNameDraft,
      setEmailDraft,
      setNameMessage,
      setValue: inputs.setValue,
      submitName,
      openEmailModal: () => setEmailModalOpen(true),
    }),
    onEmailSent: (address: string) => {
      setEmailSentTo(address);
      setBanner(t("settings.profile.emailSent", { email: address }));
      setEmailModalOpen(false);
    },
  };
}

export function useEmailConfirm(): { status: EmailConfirmStatus; dismiss: () => void } {
  const start = useStartSession();
  const showCodes = useShowRecoveryCodes();
  const bound = useSessionBound("identity");
  const client = useQueryClient();
  const handled = useRef<string | null>(null);
  const actionsRef = useRef({ start, showCodes, bound, client });
  actionsRef.current = { start, showCodes, bound, client };
  const [status, setStatus] = useState<EmailConfirmStatus>({ state: "idle" });

  useEffect(() => {
    const run = () => {
      const token = readEmailChangeToken(window.location.hash);
      if (!token || handled.current === token) return;
      handled.current = token;
      window.history.replaceState(null, "", stripEmailChangeHash(window.location));
      setStatus({ state: "pending" });
      const current = actionsRef.current;
      void current
        .bound((signal) =>
          unwrap(consoleApi.POST("/api/console/v1/profile/email/confirm", { body: { token }, signal })),
        )
        .then(async (answer) => {
          if (!answer) return;
          const { csrf_token, recovery_codes } = answer.value;
          if (recovery_codes?.length && csrf_token) {
            current.showCodes({ csrf_token, recovery_codes }, answer.claim.identity);
          }
          await current.start(csrf_token ?? "").catch(() => {});
          await current.client.invalidateQueries({ queryKey: [accountKey] });
          setStatus({ state: "confirmed" });
        })
        .catch((error: unknown) => setStatus({ state: "error", error }));
    };
    run();
    window.addEventListener("hashchange", run);
    return () => window.removeEventListener("hashchange", run);
  }, []);

  return { status, dismiss: () => setStatus({ state: "idle" }) };
}
