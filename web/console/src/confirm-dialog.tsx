import { useErrors } from "@axiom/app";
import { Button, Cluster, Dialog, FormError, Stack, SubmitButton, Text } from "@axiom/ui";
import type { UseMutationResult } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

// ConfirmDialog asks before an action that removes something from use, runs it, and says why when the server refuses.
export function ConfirmDialog({
  title,
  body,
  confirm,
  action,
  detail,
  onClose,
}: {
  title: string;
  body: ReactNode;
  confirm: string;
  action: UseMutationResult<unknown, Error, void>;
  // What the dialog says about a refused change, under the reason.
  detail?: ReactNode;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <Dialog title={title} onClose={onClose}>
      <Stack>
        {action.isError ? <FormError>{errors.message(action.error)}</FormError> : null}
        {detail}
        <Text as="div" tone="muted">
          <Stack gap={8}>{body}</Stack>
        </Text>
        <Cluster justify="end">
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
        </Cluster>
      </Stack>
    </Dialog>
  );
}
