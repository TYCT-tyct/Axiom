import { MfaPage } from "@axiom/app";
import { useAdminBrand } from "../brand";

export default function Mfa() {
  return <MfaPage brand={useAdminBrand()} />;
}
