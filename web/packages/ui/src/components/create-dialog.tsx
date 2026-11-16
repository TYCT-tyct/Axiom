import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { useExplicitSave } from "../write";
import { ActionButton } from "./action-button";
import { Sheet, SubmitActions } from "./sheet";
import { SubmitFailed } from "./submit-failed";

// CreateDialog makes something in two steps, as the board's new-key dialog does: a form, then, when there is one, a
// result shown once (a new key's secret), which only its Done button ends. The form is a draft of the whole request,
// sent once; while it is out the form is locked; a failure keeps the draft and says why above it. Esc and the
// backdrop close it only while nothing typed would be lost and nothing is on its way.
export function CreateDialog<D, R>({
  title,
  subtitle,
  initial,
  submitLabel,
  create,
  result,
  onClose,
  children,
}: {
  title: string;
  subtitle?: string;
  initial: D;
  // submitLabel names the action, 创建 by default.
  submitLabel?: string;
  create: (draft: D) => Promise<R>;
  result?: (created: R) => { title: string; subtitle?: string; body: ReactNode };
  // onClose gets what was created, or nothing when the dialog was left before.
  onClose: (created?: R) => void;
  // children are the fields, given the draft, a way to replace it, and the last failure to mark fields by.
  children: (draft: D, set: (next: D) => void, failure: unknown) => ReactNode;
}) {
  const { t } = useTranslation("common");
  const write = useExplicitSave({ initial, save: create });
  const [created, setCreated] = useState<{ saved: R } | null>(null);
  const shown = created && result ? result(created.saved) : null;
  return (
    <Sheet
      title={shown?.title ?? title}
      subtitle={shown ? shown.subtitle : subtitle}
      done={shown !== null}
      dismissible={!created && !write.saving && !write.dirty}
      locked={write.saving}
      step={shown ? 1 : 0}
      // the result step sends nothing: what was made is made once
      onSubmit={
        shown
          ? undefined
          : (close) => {
              void write.save().then((done) => {
                if (done) {
                  setCreated(done);
                  if (!result) {
                    close();
                  }
                }
              });
            }
      }
      onClose={() => onClose(created?.saved)}
      footer={(close) =>
        shown ? (
          <ActionButton variant="pri" onClick={close}>
            {t("overlay.done")}
          </ActionButton>
        ) : (
          <SubmitActions
            label={submitLabel ?? t("overlay.create")}
            busy={t("overlay.sending")}
            saving={write.saving}
            failed={write.failure !== null}
            close={close}
          />
        )
      }
    >
      {shown ? (
        shown.body
      ) : (
        <>
          {write.failure ? <SubmitFailed error={write.failure} /> : null}
          {children(write.draft, write.set, write.failure)}
        </>
      )}
    </Sheet>
  );
}
