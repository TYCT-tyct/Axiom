import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { ActionButton, Cluster, DataState, Field, ImageBox, SettingRow, Stack, Text } from "@axiom/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "./change";
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

// LogoRow is the site's logo in the brand card: the image, and the two things done to it. It is a file, so it is not a
// draft: it is set or cleared through its own dialog. A node of the release before has no logo operation, and the row
// is then not there.
export function LogoRow({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const logo = useQuery(siteLogoQuery(siteId));
  if (logo.error instanceof ApiError && logo.error.status === 404) {
    return null;
  }
  return <DataState query={logo}>{(view) => <LogoControls siteId={siteId} logo={view.logo} />}</DataState>;
}

function LogoControls({ siteId, logo }: { siteId: AdminSchemas["SiteId"]; logo: Logo | undefined }) {
  const { t } = useTranslation();
  const [setting, setSetting] = useState(false);
  const [clearing, setClearing] = useState(false);
  return (
    <>
      <SettingRow title={t("settings.logo.title")} detail={t("settings.logo.hint")}>
        {logo ? <Current logo={logo} /> : <Text tone="muted">{t("settings.logo.none")}</Text>}
        <Cluster>
          <ActionButton size="sm" onClick={() => setSetting(true)}>
            {t(logo ? "settings.logo.replace" : "settings.logo.set")}
          </ActionButton>
          {logo ? (
            <ActionButton size="sm" variant="gho" onClick={() => setClearing(true)}>
              {t("settings.logo.clear")}
            </ActionButton>
          ) : null}
        </Cluster>
      </SettingRow>
      {setting ? <SetLogo siteId={siteId} had={logo !== undefined} onClose={() => setSetting(false)} /> : null}
      {clearing ? <ClearLogo siteId={siteId} onClose={() => setClearing(false)} /> : null}
    </>
  );
}

function Current({ logo }: { logo: Logo }) {
  if (!logo.data_url) {
    return <Text tone="muted">{logo.content_type}</Text>;
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

function SetLogo({ siteId, had, onClose }: { siteId: AdminSchemas["SiteId"]; had: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const queryClient = useQueryClient();
  const { chosen, problem, choose } = useLogoFile();
  return (
    <ChangeDialog
      operation={`site-logo:${siteId}`}
      title={t("settings.logo.setTitle")}
      intro={<p>{t("settings.logo.intro")}</p>}
      diff={[
        {
          label: t("settings.logo.title"),
          before: t(had ? "settings.logo.current" : "settings.logo.nothing"),
          after: chosen?.name ?? "—",
        },
      ]}
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
      diff={[
        { label: t("settings.logo.title"), before: t("settings.logo.current"), after: t("settings.logo.nothing") },
      ]}
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
