import type { AdminSchemas } from "@axiom/api";
import { Field, Fieldset } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import type { Problem } from "../change";

type Login = AdminSchemas["SupplierLogin"];
type LoginPart = AdminSchemas["LoginField"];

// What a supplier login is made of. Each part is a secret the supplier gave, sealed on arrival and never shown again,
// so none of them is echoed back or remembered by the browser.
const secret = new Set<LoginPart>(["password", "access_token", "refresh_token"]);

function control(field: LoginPart) {
  if (field === "access_token_expires_at") {
    return { type: "datetime-local" } as const;
  }
  return { type: secret.has(field) ? "password" : "text", spellCheck: false } as const;
}

// LoginFields asks for the parts of the login that the kind of system needs, no more.
export function LoginFields({ fields }: { fields: readonly LoginPart[] }) {
  const { t } = useTranslation();
  return (
    <Fieldset legend={t("suppliers.login.legend")} hint={t("suppliers.login.hint")}>
      {fields.map((field) => (
        <Field
          key={field}
          label={t(`suppliers.login.${field}`)}
          name={`login_${field}`}
          autoComplete="off"
          maxLength={field === "email" ? 254 : 4096}
          {...control(field)}
        />
      ))}
    </Fieldset>
  );
}

// readLogin gathers the typed parts; a part left empty is left out, and a login with nothing in it is a problem.
export function readLogin(
  values: Record<string, string>,
  fields: readonly LoginPart[],
  missing: string,
): Login | Problem {
  const login: Record<string, string> = {};
  for (const field of fields) {
    const value = values[`login_${field}`] ?? "";
    if (value.trim() !== "") {
      login[field] = loginValue(field, value);
    }
  }
  return Object.keys(login).length > 0 ? (login as Login) : { problem: missing };
}

// A password is kept exactly as typed; the rest have no meaningful spaces, and a time is the viewer's own clock.
function loginValue(field: LoginPart, value: string) {
  if (field === "access_token_expires_at") {
    return new Date(value).toISOString();
  }
  return field === "password" ? value : value.trim();
}
