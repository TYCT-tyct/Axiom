import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { en, zh } from "../locales/design";
import { ActionButton } from "./action-button";
import { Placeholder, StateBlock } from "./state-block";
import { type StateFrameProps, type StateView, StateViewProvider } from "./states";

const glyphs = { empty: "overview", forbidden: "security", gone: "search", failed: "warn" } as const;

function DesignFrame({ kind, title, level, compact, requestId, when, retry, home, action, children }: StateFrameProps) {
  return (
    <StateBlock
      icon={glyphs[kind]}
      title={title}
      level={level}
      bad={kind === "failed"}
      compact={compact}
      requestId={requestId}
      actions={
        retry || home || action ? (
          <>
            {retry ? (
              <ActionButton variant="pri" onClick={retry.run}>
                {retry.label}
              </ActionButton>
            ) : null}
            {home ? <a href={home.href}>{home.label}</a> : null}
            {action}
          </>
        ) : null
      }
    >
      {children}
      {when ? <p>{when}</p> : null}
    </StateBlock>
  );
}

function DesignLoading({ page }: { page: boolean }) {
  const { t } = useTranslation("common");
  return <Placeholder label={t("state.loading")} page={page} />;
}

const view: StateView = { Frame: DesignFrame, Loading: DesignLoading };

// DesignProvider is where an app takes up the redesign (#747): DataState and the shell's states draw the design's
// StateBlock, and the messages of the design's components join the shared namespace. Only an app that imports it
// loads either, so the admin's first screen holds neither until its shell moves (A1). It wraps the whole app.
export function DesignProvider({ children }: { children: ReactNode }) {
  const { i18n } = useTranslation();
  // Before the first render of anything inside, once: a state initialiser, not an effect, which runs after it.
  useState(() => {
    i18n.addResourceBundle("zh", "common", zh, true, false);
    i18n.addResourceBundle("en", "common", en, true, false);
  });
  return <StateViewProvider value={view}>{children}</StateViewProvider>;
}
