import { createSkinEngine, localStorageSkinStorage } from "@iris-ui-kit/react";

export const platformSkin = createSkinEngine({
  skins: [],
  default: "light",
  storage: localStorageSkinStorage("aero-platform-console:skin"),
});
