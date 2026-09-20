class CloudState {
  const CloudState({required this.signedIn, required this.credits, required this.gateway});

  final bool signedIn;
  final int credits;
  final Uri? gateway;
}

abstract interface class CloudCapability {
  CloudState state();
}

CloudCapability? _cloud;

void registerCloud(CloudCapability capability) {
  _cloud = capability;
}

CloudCapability get cloud {
  final capability = _cloud;
  if (capability == null) throw StateError('Cloud capability is not registered');
  return capability;
}
