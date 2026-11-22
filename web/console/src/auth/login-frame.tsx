import { LoginLayout as Frame } from "@axiom/login/login-frame";
import { Outlet } from "@tanstack/react-router";
import { ConsoleLoginNavigation } from "../login-navigation";

export function LoginLayout() {
  return (
    <ConsoleLoginNavigation>
      <Frame>
        <Outlet />
      </Frame>
    </ConsoleLoginNavigation>
  );
}
