import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { useFormat } from "../format";
import { en, zh } from "../locales/design";
import { ActionButton } from "./action-button";
import type { GlyphName } from "./glyph";
import type { IconName } from "./icon";
import { usePageTitle } from "./page-title";
import { Placeholder, StateBlock } from "./state-block";
import {
  type ErrorProps,
  type FrameProps,
  isFailure,
  type Placement,
  type StateDesign,
  StateDesignContext,
} from "./states";

// The states' icons in the design's set.
const glyphs: Partial<Record<IconName, GlyphName>> = {
  grid: "overview",
  lock: "security",
  search: "search",
  alert: "warn",
};

// Block is a state as the design's StateBlock; one that replaces the page gives it its heading and title.
function Block({
  icon,
  title,
  bad = false,
  page = false,
  compact = false,
  actions,
  requestId,
  children,
}: {
  icon: GlyphName;
  title: string;
  bad?: boolean;
  actions?: ReactNode;
  requestId?: string | null;
  children?: ReactNode;
} & Placement) {
  usePageTitle(page ? title : undefined);
  return (
    <StateBlock
      icon={icon}
      title={title}
      level={page ? 1 : 2}
      bad={bad}
      compact={compact}
      actions={actions}
      requestId={requestId}
    >
      {children}
    </StateBlock>
  );
}

function Frame({ icon, tone, action, ...said }: FrameProps) {
  return <Block icon={glyphs[icon] ?? "overview"} bad={tone === "text-bad"} actions={action} {...said} />;
}

function Loading({ page = false }: Placement) {
  const { t } = useTranslation("common");
  return <Placeholder label={t("state.loading")} page={page} />;
}

function Retry({ label, run }: { label: string; run: () => void }) {
  return (
    <ActionButton variant="pri" onClick={run}>
      {label}
    </ActionButton>
  );
}

function ErrorBlock({ error, at, onRetry, ...placement }: ErrorProps) {
  const { t } = useTranslation("common");
  const format = useFormat();
  const failure = isFailure(error) ? error : null;
  return (
    <Block
      icon="warn"
      bad
      title={t("state.errorTitle")}
      requestId={failure?.requestId}
      actions={onRetry ? <Retry label={t("state.retry")} run={onRetry} /> : null}
      {...placement}
    >
      <p role="alert">{failure?.status === 0 ? t("state.networkError") : t("state.errorBody")}</p>
      {at ? <p>{t("state.failedAt", { time: format.dateTime(at) })}</p> : null}
    </Block>
  );
}

function PageFailed() {
  const { t } = useTranslation("common");
  return (
    <Block
      icon="warn"
      bad
      page
      title={t("state.pageFailed")}
      actions={<Retry label={t("state.reload")} run={() => window.location.reload()} />}
    >
      <p role="alert">{t("state.pageFailedBody")}</p>
    </Block>
  );
}

const design: StateDesign = { Frame, Loading, Error: ErrorBlock, PageFailed };

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
  return <StateDesignContext value={design}>{children}</StateDesignContext>;
}
