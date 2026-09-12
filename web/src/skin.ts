import { createSkinEngine, localStorageSkinStorage } from "@iris-ui-kit/vue";

export const platformSkin = createSkinEngine({
  skins: [],
  default: "light",
  storage: localStorageSkinStorage("aero-platform-console:skin"),
});
