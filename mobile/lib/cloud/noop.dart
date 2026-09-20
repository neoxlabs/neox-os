import 'contract.dart';

class NoopCloud implements CloudCapability {
  const NoopCloud();

  @override
  CloudState state() => const CloudState(signedIn: false, credits: 0, gateway: null);
}

void registerNoopCloud() {
  registerCloud(const NoopCloud());
}
