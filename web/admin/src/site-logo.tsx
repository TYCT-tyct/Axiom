import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Button, DataState, Icon, TextField, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "./change";
import { Facts } from "./parts";
import { siteLogoQuery } from "./queries";

type Logo = AdminSchemas["SiteLogo"];
type Upload = AdminSchemas["SiteLogoUpload"];

const maxLogoBytes = 256 * 1024;

// The types the API takes, and what the page calls them. The server checks the file itself; this only keeps a browser
// from offering what it cannot send.
const types = ["image/svg+xml", "image/png", "image/webp"] as const;
type LogoType = (typeof types)[number];

function isLogoType(value: string): value is LogoType {
  return (types as readonly string[]).includes(value);
}

// typeOf is the content type of a chosen file: what the browser says, or, when it says nothing, the extension.
function typeOf(file: File): LogoType | undefined {
  if (file.type !== "" && !isLogoType(file.type)) {
    return undefined;
  }
  if (isLogoType(file.type)) {
    return file.type;
  }
  const extension = file.name.split(".").pop()?.toLowerCase();
  return extension === "svg"
    ? "image/svg+xml"
    : extension === "png"
      ? "image/png"
      : extension === "webp"
        ? "image/webp"
        : undefined;
}

// base64 is the file's bytes as the contract's byte string. A logo is at most 256 KiB, but encoding it in one spread
// would still pass an argument list that size.
function base64(data: ArrayBuffer): string {
  const bytes = new Uint8Array(data);
  let binary = "";
  for (let at = 0; at < bytes.length; at += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(at, at + 0x8000));
  }
  return btoa(binary);
}

const names: Record<LogoType, string> = { "image/svg+xml": "SVG", "image/png": "PNG", "image/webp": "WebP" };

export function SiteLogo({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  const logo = useQuery(siteLogoQuery(siteId));
  const [setting, setSetting] = useState(false);
  const [clearing, setClearing] = useState(false);
  if (logo.error instanceof ApiError && logo.error.status === 404) {
    return (
      <Facts
        title={t("settings.logo.title")}
        rows={[[t("settings.logo.unavailableLabel"), t("settings.logo.unavailable")]]}
      />
    );
  }
  return (
    <DataState query={logo}>
      {(view) => {
        const rows: [string, ReactNode][] = [
          [
            t("settings.logo.current"),
            view.logo ? <Current logo={view.logo} /> : <span className="text-t2">{t("settings.logo.none")}</span>,
          ],
        ];
        if (view.logo) {
          rows.push([
            t("settings.logo.details"),
            <span key="details" className="text-t2">
              {names[view.logo.content_type as LogoType] ?? view.logo.content_type}
              {view.logo.width > 0 && view.logo.height > 0
                ? ` · ${t("settings.logo.pixels", { width: view.logo.width, height: view.logo.height })}`
                : ""}
              {` · ${t("settings.logo.bytes", { size: format.number(view.logo.size) })}`}
            </span>,
          ]);
        }
        return (
          <>
            <Facts
              title={t("settings.logo.title")}
              action={
                <div className="flex flex-wrap items-center gap-2">
                  <Button size="sm" onClick={() => setSetting(true)}>
                    <Icon name="pencil" />
                    {t("settings.logo.set")}
                  </Button>
                  {view.logo ? (
                    <Button size="sm" variant="ghost" onClick={() => setClearing(true)}>
                      {t("settings.logo.clear")}
                    </Button>
                  ) : null}
                </div>
              }
              rows={rows}
            />
            {setting ? <SetLogo siteId={siteId} onClose={() => setSetting(false)} /> : null}
            {clearing ? <ClearLogo siteId={siteId} onClose={() => setClearing(false)} /> : null}
          </>
        );
      }}
    </DataState>
  );
}

function Current({ logo }: { logo: Logo }) {
  if (!logo.data_url) {
    return <span className="text-t2">{logo.content_type}</span>;
  }
  return (
    <img
      src={logo.data_url}
      alt=""
      width={40}
      height={40}
      className="size-10 shrink-0 rounded-btn border border-b1 bg-s2 object-contain"
    />
  );
}

// useLogoFile reads a chosen file into the contract's upload shape, with a preview, and says what is wrong with one it
// cannot send. A later choice wins over a slower read of an earlier one.
function useLogoFile() {
  const { t } = useTranslation();
  const [chosen, setChosen] = useState<{ upload: Upload; preview: string; name: string } | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const picking = useRef(0);
  const choose = async (file: File | undefined) => {
    const sequence = ++picking.current;
    setChosen(null);
    setProblem(null);
    if (!file) {
      return;
    }
    const type = typeOf(file);
    if (!type) {
      setProblem(t("settings.logo.unsupported"));
      return;
    }
    if (file.size > maxLogoBytes) {
      setProblem(t("settings.logo.tooLarge"));
      return;
    }
    try {
      const data = base64(await file.arrayBuffer());
      if (sequence !== picking.current) {
        return;
      }
      setChosen({
        upload: { content_type: type, data_base64: data },
        preview: `data:${type};base64,${data}`,
        name: file.name,
      });
    } catch {
      if (sequence === picking.current) {
        setProblem(t("settings.logo.unreadable"));
      }
    }
  };
  return { chosen, problem, choose };
}

function SetLogo({ siteId, onClose }: { siteId: AdminSchemas["SiteId"]; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const { chosen, problem, choose } = useLogoFile();
  return (
    <ChangeDialog
      operation={`site-logo:${siteId}`}
      title={t("settings.logo.setTitle")}
      intro={<p>{t("settings.logo.intro")}</p>}
      action={t("settings.logo.save")}
      fields={(error) => (
        <div className="space-y-3">
          <TextField
            label={t("settings.logo.file")}
            hint={t("settings.logo.hint")}
            name="file"
            type="file"
            accept={types.join(",")}
            onChange={(event) => void choose(event.currentTarget.files?.[0])}
            error={problem ?? errors.field(error, "logo")}
          />
          {chosen ? (
            <div className="flex items-center gap-3 rounded-btn border border-b1 bg-s2 p-3">
              <img src={chosen.preview} alt="" width={48} height={48} className="size-12 shrink-0 object-contain" />
              <span className="min-w-0 text-t2 [overflow-wrap:anywhere]">{chosen.name}</span>
            </div>
          ) : null}
        </div>
      )}
      read={() => (chosen ? chosen.upload : { problem: t("settings.logo.choose") })}
      send={(logo, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/logo", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { logo, reason },
          }),
        )
      }
      done={() => <p>{t("settings.logo.saved")}</p>}
      onClose={onClose}
    />
  );
}

function ClearLogo({ siteId, onClose }: { siteId: AdminSchemas["SiteId"]; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`site-logo-clear:${siteId}`}
      title={t("settings.logo.clearTitle")}
      intro={<p>{t("settings.logo.clearIntro")}</p>}
      action={t("settings.logo.clear")}
      danger
      fields={() => <p className="text-t2">{t("settings.logo.clearHint")}</p>}
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/logo", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={() => <p>{t("settings.logo.cleared")}</p>}
      onClose={onClose}
    />
  );
}
