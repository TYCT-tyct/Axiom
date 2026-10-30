import { MfaPage } from "@axiom/app";
import { useQuery } from "@tanstack/react-query";
import { siteQuery } from "../queries";
import { PasskeyMfa } from "./passkey-mfa";

export default function Mfa() {
  const site = useQuery(siteQuery);
  return <MfaPage brand={site.data} extra={site.data?.passkeys ? <PasskeyMfa /> : null} />;
}
