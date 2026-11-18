import type { ReactNode } from "react";
import EmptyRegion from "./AxApp/regions/PartEmpty.tsx";
import ErrorRegion from "./AxApp/regions/PartError.tsx";
import LoadingRegion from "./AxApp/regions/PartLoading.tsx";
import LoadingFrame from "./AxApp/regions/PartLoadingFrame.tsx";
import NoPermissionRegion from "./AxApp/regions/PartNoPermission.tsx";

type Action = { label: string; onClick: () => void; disabled?: boolean };
type Message = { title: string; description?: string };

export function Loading({ label, children }: { label: string; children: ReactNode }) {
  return (
    <LoadingFrame t={String}>
      <LoadingRegion t={String} values={{ label }}>
        {children}
      </LoadingRegion>
    </LoadingFrame>
  );
}

function stateProps({ title, description }: Message, primary?: Action, secondary?: Action) {
  return {
    t: String,
    text: { title, description: description ?? "", primary: primary?.label ?? "", secondary: secondary?.label ?? "" },
    values: {
      hideDescription: !description,
      hideActions: !primary && !secondary,
      hidePrimary: !primary,
      hideSecondary: !secondary,
      pending: primary?.disabled,
      primary: primary?.onClick,
      secondary: secondary?.onClick,
    },
  };
}

export function Empty({ action, ...message }: Message & { action?: Action }) {
  return <EmptyRegion {...stateProps(message, action)} />;
}

export function ErrorState({ retry, ...message }: Message & { retry: Action }) {
  return <ErrorRegion {...stateProps(message, retry)} />;
}

export function NoPermission({ action, ...message }: Message & { action?: Action }) {
  return <NoPermissionRegion {...stateProps(message, action)} />;
}
