import type { AdminSchemas } from "@axiom/api";
import type { TFunction } from "i18next";
import { isProblem, type Problem } from "../change";

type Proxy = AdminSchemas["EgressProxy"];
type Create = Omit<AdminSchemas["EgressProxyCreate"], "reason">;
type Update = Omit<AdminSchemas["EgressProxyUpdate"], "reason">;
type Auth = AdminSchemas["EgressProxyAuth"];
type T = TFunction<"admin">;

// The limits are those of the contract's request schemas (api/admin.yaml, EgressProxyCreate and EgressProxyUpdate).
const idPattern = /^[a-z0-9][a-z0-9_-]{0,31}$/;
const hostPattern = /^[A-Za-z0-9.:-]{1,253}$/;
const schemes = ["http", "https", "socks5"] as const;

export type Usage = { credentials: number; fallbackOf: number };

// usage says what keeps a proxy from being removed: the live credentials that leave by it, and the proxies that name it
// as their fallback.
export function usage(proxy: Proxy, proxies: readonly Proxy[], exits: readonly string[]): Usage {
  return {
    credentials: exits.filter((id) => id === proxy.id).length,
    fallbackOf: proxies.filter((other) => other.fallback_id === proxy.id).length,
  };
}

const text = (values: Record<string, string>, key: string) => values[key]?.trim() ?? "";

// readAuth is the login to replace the stored one with: nothing typed keeps it. A password only means something with
// the name it belongs to, and a name with no password sets none.
function readAuth(values: Record<string, string>, t: T): Auth | undefined | Problem {
  const username = text(values, "username");
  const password = values.password ?? "";
  if (!username && !password) return undefined;
  if (!username || username.length > 255 || password.length > 255) return { problem: t("egress.invalidAuth") };
  return password ? { username, password } : { username };
}

function readName(values: Record<string, string>, t: T): string | Problem {
  const name = text(values, "name");
  return name.length >= 1 && name.length <= 200 ? name : { problem: t("egress.invalidName") };
}

export function readCreate(values: Record<string, string>, t: T): Create | Problem {
  const id = text(values, "id");
  if (!idPattern.test(id) || id === "direct") return { problem: t("egress.invalidId") };
  const name = readName(values, t);
  if (isProblem(name)) return name;
  const scheme = schemes.find((s) => s === values.scheme);
  const host = text(values, "host");
  const port = Number(text(values, "port"));
  if (!scheme || !hostPattern.test(host)) return { problem: t("egress.invalidHost") };
  if (!Number.isInteger(port) || port < 1 || port > 65535) return { problem: t("egress.invalidPort") };
  const auth = readAuth(values, t);
  if (isProblem(auth)) return auth;
  const status = values.status === "disabled" ? "disabled" : "active";
  return { id, name, scheme, host, port, auth, fallback_id: values.fallback_id || undefined, status };
}

export function readUpdate(values: Record<string, string>, t: T): Update | Problem {
  const name = readName(values, t);
  if (isProblem(name)) return name;
  const auth = readAuth(values, t);
  if (isProblem(auth)) return auth;
  return {
    name,
    status: values.status === "disabled" ? "disabled" : "active",
    fallback_id: values.fallback_id || null,
    auth,
  };
}
