import { MfaPage } from "@axiom/app";
import { useQuery } from "@tanstack/react-query";
import { siteQuery } from "../queries";

export default function Mfa() {
  return <MfaPage brand={useQuery(siteQuery).data} />;
}
