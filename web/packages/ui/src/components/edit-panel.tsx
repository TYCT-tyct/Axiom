import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { ExplicitSave, Failed, LiveSave } from "../write";
import { ActionButton } from "./action-button";
import { Note } from "./note";
import { Sheet, SubmitActions } from "./sheet";
import { SubmitFailed } from "./submit-failed";

// What EditPanel reads of a write strategy (write.ts); the business object chose the strategy.
type PanelWrite =
  | Pick<ExplicitSave<unknown, unknown>, "mode" | "dirty" | "saving" | "failure" | "save">
  | Pick<LiveSave<unknown>, "mode" | "saving" | "saved" | "failed" | "locked">;

// EditPanel edits an object in a panel down the right edge, a sheet from the bottom on a phone. It only presents: the
// page passes the write strategy and renders the fields from it. With explicit save the footer is Cancel and Save, the
// fields are locked while the save is out, and it closes once saved; with save on change the footer says whether the
// changes have landed and Done closes it. While a write is out nothing closes it and start (a link at the start of the
// footer, such as delete) is disabled; Esc and the backdrop also wait for nothing unsaved to be lost. While the object
// is not known after a write whose result is unknown, the fields are locked.
export function EditPanel({
  title,
  subtitle,
  write,
  start,
  onClose,
  children,
}: {
  title: string;
  subtitle?: string;
  write: PanelWrite;
  start?: ReactNode;
  onClose: () => void;
  children: ReactNode;
}) {
  const { t } = useTranslation("common");
  const explicit = write.mode === "explicit";
  return (
    <Sheet
      title={title}
      subtitle={subtitle}
      side
      dismissible={!write.saving && !(explicit && write.dirty)}
      locked={explicit ? write.saving : write.locked}
      busy={write.saving}
      onSubmit={
        explicit
          ? (close) => {
              void write.save().then((done) => done && close());
            }
          : undefined
      }
      start={start}
      status={
        explicit
          ? undefined
          : {
              text: write.saving ? t("overlay.saving") : write.saved ? t("overlay.saved") : t("overlay.live"),
              saved: write.saved,
            }
      }
      onClose={onClose}
      footer={(close) =>
        explicit ? (
          <SubmitActions
            label={t("overlay.save")}
            busy={t("overlay.saving")}
            saving={write.saving}
            failed={write.failure !== null}
            close={close}
          />
        ) : (
          <ActionButton variant="pri" disabled={write.saving} onClick={close}>
            {t("overlay.done")}
          </ActionButton>
        )
      }
    >
      {explicit ? (
        write.failure ? (
          <SubmitFailed error={write.failure} />
        ) : null
      ) : write.failed ? (
        <LiveFailed failed={write.failed} />
      ) : null}
      {children}
    </Sheet>
  );
}

// LiveFailed says what a failed change left: someone else's change, shown now; the server's object again; or, when the
// result is unknown, nothing known until the page is reloaded.
function LiveFailed({ failed }: { failed: Failed }) {
  const { t } = useTranslation("common");
  if (failed.after === "reloaded") {
    return <Note title={t("overlay.staleTitle")}>{t("overlay.staleBody")}</Note>;
  }
  return <SubmitFailed error={failed.error} next={t(`failed.${failed.after}`)} />;
}
