import { EnrollPage } from "@axiom/app";
import { useAdminBrand } from "../brand";

export default function Enroll() {
  return <EnrollPage brand={useAdminBrand()} />;
}
