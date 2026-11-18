import DesktopProfile from "@axiom/boards/AxApp/regions/Profile";
import DesktopProfileEn from "@axiom/boards/AxApp/regions/Profile.en";
import DesktopProfileZh from "@axiom/boards/AxApp/regions/Profile.zh";
import DesktopSecurity from "@axiom/boards/AxApp/regions/Security";
import DesktopSecurityEn from "@axiom/boards/AxApp/regions/Security.en";
import DesktopSecurityZh from "@axiom/boards/AxApp/regions/Security.zh";
import PhoneProfile from "@axiom/boards/AxAppPhone/regions/Profile";
import PhoneProfileEn from "@axiom/boards/AxAppPhone/regions/Profile.en";
import PhoneProfileZh from "@axiom/boards/AxAppPhone/regions/Profile.zh";
import PhoneSecurity from "@axiom/boards/AxAppPhone/regions/Security";
import PhoneSecurityEn from "@axiom/boards/AxAppPhone/regions/Security.en";
import PhoneSecurityZh from "@axiom/boards/AxAppPhone/regions/Security.zh";
import type { DOMAttributes, RefCallback, SyntheticEvent } from "react";
import { boardPageStyle, makeBoardT } from "./account-board";

const emptyAuthRows = [] as const;
type ActionRows = Parameters<typeof DesktopSecurity>[0]["rows"]["sessions0"];
type InputRows = Parameters<typeof DesktopProfile>[0]["rows"]["rows0"];

// Reuse the board's titled .qcard.rows + optional header button layout across settings, security, and notifications.
export function AccountActionCard({
  wide,
  english,
  title,
  actionLabel = "",
  actionDisabled = false,
  onAction,
  rowDisabled = false,
  onRowAction,
  rows,
  last = false,
}: {
  wide: boolean;
  english: boolean;
  title: string;
  actionLabel?: string;
  actionDisabled?: boolean;
  onAction?: (event: SyntheticEvent) => void;
  rowDisabled?: boolean;
  onRowAction?: (event: SyntheticEvent) => void;
  rows: ActionRows;
  last?: boolean;
}) {
  const Region = wide ? DesktopSecurity : PhoneSecurity;
  const t = makeBoardT(english, wide ? DesktopSecurityEn : PhoneSecurityEn, wide ? DesktopSecurityZh : PhoneSecurityZh);
  return (
    <Region
      t={t}
      values={{
        pageStyle: boardPageStyle(wide, { last }),
        hideHead: true,
        hideCard1: true,
        hideRevokeOthers: actionLabel === "",
        revokeOthersDisabled: actionDisabled,
        onRevokeOthers: onAction,
        sessionRevokeDisabled: rowDisabled,
        onSessionRevoke: onRowAction,
      }}
      rows={{ authRows0: emptyAuthRows, sessions0: rows }}
      text={{ sessionsTitle: title, sessionsClearLabel: actionLabel }}
    />
  );
}

// Reuse the board's headless .qcard.rows input card for passkey password and notification delivery inputs.
export function AccountInputsCard({
  wide,
  english,
  inputRef,
  disabled,
  onInput,
  onKeyDown,
  rows,
}: {
  wide: boolean;
  english: boolean;
  inputRef: RefCallback<Element>;
  disabled?: boolean;
  onInput?: DOMAttributes<Element>["onInput"];
  onKeyDown?: DOMAttributes<Element>["onKeyDown"];
  rows: InputRows;
}) {
  const Region = wide ? DesktopProfile : PhoneProfile;
  const t = makeBoardT(english, wide ? DesktopProfileEn : PhoneProfileEn, wide ? DesktopProfileZh : PhoneProfileZh);
  return (
    <Region
      t={t}
      values={{
        pageStyle: boardPageStyle(wide),
        hideHead: true,
        hideDeletion: true,
        inputRef,
        inputDisabled: disabled,
        onInput,
        onKeyDown,
      }}
      rows={{ rows0: rows }}
    />
  );
}
