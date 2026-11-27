import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Field, Grid, Select, Text } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import { Code } from "../parts";
import { readCreate, readUpdate, type Usage } from "./read";

type Proxy = AdminSchemas["EgressProxy"];

const schemes: readonly Proxy["scheme"][] = ["http", "https", "socks5"];

// A fallback is another proxy with no fallback of its own (api/admin.yaml, createEgressProxy).
const fallbacks = (proxies: readonly Proxy[], self?: string) => proxies.filter((p) => p.id !== self && !p.fallback_id);

function Fallback({ proxies, self, current }: { proxies: readonly Proxy[]; self?: string; current?: string }) {
  const { t } = useTranslation();
  return (
    <Select
      label={t("egress.fallback")}
      hint={t("egress.fallbackHint")}
      name="fallback_id"
      defaultValue={current ?? ""}
    >
      <option value="">{t("egress.noFallback")}</option>
      {fallbacks(proxies, self).map((p) => (
        <option key={p.id} value={p.id}>
          {t("egress.option", { name: p.name, id: p.id })}
        </option>
      ))}
    </Select>
  );
}

function StatusSelect({ current }: { current: Proxy["status"] }) {
  const { t } = useTranslation();
  return (
    <Select label={t("egress.status")} name="status" defaultValue={current}>
      <option value="active">{t("egress.statuses.active")}</option>
      <option value="disabled">{t("egress.statuses.disabled")}</option>
    </Select>
  );
}

// The login fields are write-only: the password is never read back, so the form cannot show it. A proxy's edit says
// whether one is kept and that typing a login replaces it.
function Login({ error, proxy }: { error: unknown; proxy?: Proxy }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <Grid min="16rem">
      <Field
        label={t("egress.username")}
        hint={proxy ? t("egress.usernameKeep", { name: proxy.username ?? t("egress.none") }) : undefined}
        name="username"
        autoComplete="off"
        maxLength={255}
        error={errors.field(error, "auth.username")}
      />
      <Field
        label={t("egress.password")}
        hint={proxy ? t(proxy.has_password ? "egress.passwordKept" : "egress.passwordNone") : t("egress.passwordHint")}
        name="password"
        type="password"
        autoComplete="new-password"
        maxLength={255}
        error={errors.field(error, "auth.password")}
      />
    </Grid>
  );
}

export function CreateProxy({ proxies, onClose }: { proxies: readonly Proxy[]; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <ChangeDialog
      operation="egress-proxy-create"
      title={t("egress.createTitle")}
      intro={<p>{t("egress.createIntro")}</p>}
      action={t("egress.create")}
      fields={(error) => (
        <>
          <Grid min="16rem">
            <Field
              label={t("egress.id")}
              hint={t("egress.idHint")}
              name="id"
              maxLength={32}
              autoComplete="off"
              error={errors.field(error, "id")}
            />
            <Field label={t("egress.name")} name="name" maxLength={200} error={errors.field(error, "name")} />
          </Grid>
          <Grid min="12rem">
            <Select label={t("egress.scheme")} name="scheme" defaultValue="http">
              {schemes.map((scheme) => (
                <option key={scheme} value={scheme}>
                  {scheme}
                </option>
              ))}
            </Select>
            <Field
              label={t("egress.host")}
              hint={t("egress.hostHint")}
              name="host"
              maxLength={253}
              autoComplete="off"
              error={errors.field(error, "host")}
            />
            <Field
              label={t("egress.port")}
              name="port"
              type="number"
              min={1}
              max={65535}
              error={errors.field(error, "port")}
            />
          </Grid>
          <Login error={error} />
          <Fallback proxies={proxies} />
          <StatusSelect current="active" />
        </>
      )}
      read={(values) => readCreate(values, t)}
      send={(input, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/egress-proxies", {
            params: { header: { "Idempotency-Key": key } },
            body: { ...input, reason },
          }),
        )
      }
      done={(made) => <p>{t("egress.created", { name: made.name })}</p>}
      onClose={onClose}
    />
  );
}

export function EditProxy({
  proxy,
  proxies,
  onClose,
}: {
  proxy: Proxy;
  proxies: readonly Proxy[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <ChangeDialog
      operation={`egress-proxy:${proxy.id}`}
      title={t("egress.editTitle", { name: proxy.name })}
      intro={
        <p>
          <Code>{proxy.id}</Code> {t("egress.editIntro", { scheme: proxy.scheme, host: proxy.host, port: proxy.port })}
        </p>
      }
      action={t("egress.save")}
      fields={(error) => (
        <>
          <Field
            label={t("egress.name")}
            name="name"
            defaultValue={proxy.name}
            maxLength={200}
            error={errors.field(error, "name")}
          />
          <StatusSelect current={proxy.status} />
          <Fallback proxies={proxies} self={proxy.id} current={proxy.fallback_id} />
          <Login error={error} proxy={proxy} />
        </>
      )}
      read={(values) => readUpdate(values, t)}
      send={(input, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/egress-proxies/{proxy_id}", {
            params: { path: { proxy_id: proxy.id }, header: { "Idempotency-Key": key } },
            body: { ...input, reason },
          }),
        )
      }
      done={(made) => <p>{t("egress.saved", { name: made.name })}</p>}
      onClose={onClose}
    />
  );
}

// A proxy in use is refused by the server (409); the dialog says so beforehand and still sends, so the refusal the
// contract gives is what the person reads, and a use the page could not see is still caught.
export function RemoveProxy({ proxy, use, onClose }: { proxy: Proxy; use: Usage; onClose: () => void }) {
  const { t } = useTranslation();
  const inUse = use.credentials + use.fallbackOf > 0;
  return (
    <ChangeDialog
      operation={`egress-proxy-remove:${proxy.id}`}
      title={t("egress.removeTitle", { name: proxy.name })}
      intro={<p>{t("egress.removeIntro")}</p>}
      warn={inUse ? t("egress.removeInUse", use) : undefined}
      action={t("egress.remove")}
      danger
      confirm={proxy.id}
      read={() => proxy.id}
      send={(id, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/egress-proxies/{proxy_id}/removal", {
            params: { path: { proxy_id: id }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={(gone) => <Text tone="muted">{t("egress.removed", { name: gone.name })}</Text>}
      onClose={onClose}
    />
  );
}
