# Browser-approved gateway client fixture

`client.mjs` consumes only the published HTTPS API. Register a named gateway in
Plugins, save its one-time client secret in protected operator configuration,
and construct `GatewayClient(origin, clientId, secret)`.

Call `begin` with an exact project, action list, template/instance IDs and target
IDs. Display the returned verification URI and user code. The user signs in and
approves that exact scope. Wait at least the returned polling interval; add five
seconds after each `slow_down`. Stop on denial, expiry, or successful delivery.
The grant lasts at most 15 minutes, has no refresh token, and every API call
requires the registered client credentials and current user authority. Users
revoke grants in Your settings; administrators can disable a client in Plugins.

Keep device codes, client secrets and access tokens out of URLs, logs and files
other than protected client configuration. The optional CA constructor argument
exists for an operator's private CA and the local conformance fixture; certificate
verification remains enabled. This fixture does not implement SSH or service accounts.
