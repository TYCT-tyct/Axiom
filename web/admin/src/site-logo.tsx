import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { ActionButton, Cluster, DataState, Field, ImageBox, Stack, Text, useFormat } from "@axiom/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "./change";
import { Facts } from "./parts";
import { adminSiteQuery, siteLogoQuery } from "./queries";

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
            view.logo ? (
              <Current logo={view.logo} />
            ) : (
              <Text as="span" tone="muted">
                {t("settings.logo.none")}
              </Text>
            ),
          ],
        ];
        if (view.logo) {
          rows.push([t("settings.logo.details"), <LogoDetails key="details" logo={view.logo} />]);
        }
        return (
          <>
            <Facts
              title={t("settings.logo.title")}
              actions={
                <Cluster>
                  <ActionButton onClick={() => setSetting(true)} icon="pencil">
                    {t("settings.logo.set")}
                  </ActionButton>
                  {view.logo ? (
                    <ActionButton variant="gho" onClick={() => setClearing(true)}>
                      {t("settings.logo.clear")}
                    </ActionButton>
                  ) : null}
                </Cluster>
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
    return (
      <Text as="span" tone="muted">
        {logo.content_type}
      </Text>
    );
  }
  return <ImageBox src={logo.data_url} size={40} />;
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
  const queryClient = useQueryClient();
  const { chosen, problem, choose } = useLogoFile();
  return (
    <ChangeDialog
      operation={`site-logo:${siteId}`}
      title={t("settings.logo.setTitle")}
      intro={<p>{t("settings.logo.intro")}</p>}
      action={t("settings.logo.save")}
      fields={(error) => (
        <Stack gap={12}>
          <Field
            label={t("settings.logo.file")}
            hint={t("settings.logo.hint")}
            name="file"
            type="file"
            accept={types.join(",")}
            onChange={(event) => void choose(event.currentTarget.files?.[0])}
            error={problem ?? errors.field(error, "logo")}
          />
          {chosen ? (
            <Cluster gap={12}>
              <ImageBox src={chosen.preview} size={48} />
              <Text as="span" tone="muted">
                {chosen.name}
              </Text>
            </Cluster>
          ) : null}
        </Stack>
      )}
      read={() => (chosen ? chosen.upload : { problem: t("settings.logo.choose") })}
      send={async (logo, { reason, key }) => {
        const sent = await unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/logo", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { logo, reason },
          }),
        );
        await queryClient.invalidateQueries({ queryKey: adminSiteQuery.queryKey });
        return sent;
      }}
      done={() => <p>{t("settings.logo.saved")}</p>}
      onClose={onClose}
    />
  );
}

function ClearLogo({ siteId, onClose }: { siteId: AdminSchemas["SiteId"]; onClose: () => void }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  return (
    <ChangeDialog
      operation={`site-logo-clear:${siteId}`}
      title={t("settings.logo.clearTitle")}
      intro={<p>{t("settings.logo.clearIntro")}</p>}
      action={t("settings.logo.clear")}
      danger
      fields={() => <Text tone="muted">{t("settings.logo.clearHint")}</Text>}
      read={() => ({})}
      send={async (_, { reason, key }) => {
        const sent = await unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/logo", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        );
        await queryClient.invalidateQueries({ queryKey: adminSiteQuery.queryKey });
        return sent;
      }}
      done={() => <p>{t("settings.logo.cleared")}</p>}
      onClose={onClose}
    />
  );
}

// LogoDetails is what the stored image is: its type, its size in pixels when known, and its bytes.
function LogoDetails({ logo }: { logo: Logo }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Text as="span" tone="muted">
      {names[logo.content_type as LogoType] ?? logo.content_type}
      {logo.width > 0 && logo.height > 0
        ? ` · ${t("settings.logo.pixels", { width: logo.width, height: logo.height })}`
        : ""}
      {` · ${t("settings.logo.bytes", { size: format.number(logo.size) })}`}
    </Text>
  );
}
