import { useErrors } from "@axiom/app";
import { Button, Dialog, FormError, SubmitButton } from "@axiom/ui";
import type { UseMutationResult } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

// ConfirmDialog asks before an action that removes something from use, runs it, and says why when the server refuses.
export function ConfirmDialog({
  title,
  body,
  confirm,
  action,
  onClose,
}: {
  title: string;
  body: string;
  confirm: string;
  action: UseMutationResult<unknown, Error, void>;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <Dialog title={title} onClose={onClose}>
      <div className="space-y-4">
        {action.isError ? <FormError>{errors.message(action.error)}</FormError> : null}
        <p className="text-t2 [overflow-wrap:anywhere]">{body}</p>
        <div className="flex justify-end gap-2">
          <Button size="md" onClick={onClose}>
            {t("cancel", { ns: "common" })}
          </Button>
          <SubmitButton
            pending={action.isPending}
            variant="danger"
            onClick={(event) => {
              event.preventDefault();
              action.mutate();
            }}
          >
            {confirm}
          </SubmitButton>
        </div>
      </div>
    </Dialog>
  );
}
