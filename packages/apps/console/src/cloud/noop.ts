import { registerCloud, type CloudCapability } from "./contract.js";

const noop: CloudCapability = {
  state: () => ({ signedIn: false, credits: 0, gateway: null })
};

registerCloud(noop);

export { noop };
