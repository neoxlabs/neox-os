export interface CloudState {
  readonly signedIn: boolean;
  readonly credits: number;
  readonly gateway: string | null;
}

export interface CloudCapability {
  state(): CloudState;
}

let cloud: CloudCapability | undefined;

export function registerCloud(capability: CloudCapability): void {
  cloud = capability;
}

export function getCloud(): CloudCapability {
  if (cloud === undefined) throw new Error("Cloud capability is not registered");
  return cloud;
}
