import type { AdminSchemas } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Field, TextArea } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { JsonField, KeysField } from "./key-fields";
import { fieldWords } from "./supplier-types";

type TypeField = AdminSchemas["SupplierTypeField"];

// A type's fields in its descriptor's order, each drawn by its kind and labelled by its name from the page's
// vocabulary. A secret is a password box or a JSON box the page never fills back; only a setting (text) is plain.
// regionsHint is the line under the regions box, which says something else where regions are being replaced.
export function TypeFields({
  fields,
  error,
  regionsHint,
}: {
  fields: readonly TypeField[];
  error: unknown;
  regionsHint?: string;
}) {
  return fields.map((field) => (
    <TypeFieldInput key={field.name} field={field} error={error} regionsHint={regionsHint} />
  ));
}

function TypeFieldInput({ field, error, regionsHint }: { field: TypeField; error: unknown; regionsHint?: string }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const { label, hint } = fieldWords(t, field.name);
  const common = { name: field.name, label, maxLength: field.max_length, spellCheck: false, autoComplete: "off" };
  switch (field.kind) {
    case "keys":
      return <KeysField error={error} />;
    case "regions":
      return <TextArea {...common} hint={regionsHint ?? hint} rows={2} error={errors.field(error, field.name)} />;
    case "deployments":
      return (
        <TextArea
          {...common}
          hint={hint}
          rows={3}
          placeholder={t("upstream.fields.deployments.placeholder")}
          error={errors.field(error, field.name)}
        />
      );
    case "json":
      return (
        <JsonField
          name={field.name}
          label={label}
          hint={hint}
          maxLength={field.max_length}
          error={errors.field(error, `fields/${field.name}`)}
        />
      );
    case "secret":
      return <Field {...common} type="password" hint={hint} error={errors.field(error, `fields/${field.name}`)} />;
    case "text":
      return <Field {...common} hint={hint} error={errors.field(error, `fields/${field.name}`)} />;
  }
}
