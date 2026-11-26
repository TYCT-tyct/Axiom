import type { AdminSchemas } from "@axiom/api";
import type { Problem } from "./change";

type Settings = AdminSchemas["MailSettingsUpdate"]["settings"];

// readPort is the port as typed: a whole number of 1 to 65535, 587 where nothing is typed.
function readPort(text: string): number | null {
  const port = Number(text.trim() || "587");
  return Number.isInteger(port) && port >= 1 && port <= 65_535 ? port : null;
}

const optional = (text: string) => (text === "" ? undefined : text);

// readMail turns the rows' text into the request. Enabled mail needs a server and a sender, while disabled mail may keep
// blanks so it can still be turned off after an environment change; an empty password keeps the saved one.
export function readMail(values: Record<string, string>, invalidPort: string, required: string): Settings | Problem {
  const port = readPort(values.port ?? "");
  if (port === null) {
    return { problem: invalidPort };
  }
  const enabled = values.enabled === "true";
  const host = (values.host ?? "").trim();
  const from = (values.from ?? "").trim();
  if (enabled && (!host || !from)) {
    return { problem: required };
  }
  return {
    enabled,
    host: optional(host),
    port,
    username: optional((values.username ?? "").trim()),
    from: optional(from),
    password: optional(values.password ?? ""),
  };
}

// mailSavedRestart is the save's own result: a database change needs a restart before the running node uses it.
export function mailSavedRestart(saved: AdminSchemas["MailSettings"]): boolean {
  return saved.restart_required === true;
}
