# iOS App IDs, capabilities and provisioning profiles

`mobile-release profiles` keeps the App IDs, capabilities and provisioning
profiles of your iOS apps in line with what each `release.toml` declares,
through the App Store Connect API. Adding an entitlement becomes one line in
`release.toml` and one command for every app, instead of clicking through the
Apple Developer portal app by app.

- `profiles plan` is read-only: it shows, per app, missing App IDs,
  capabilities to enable and profiles that are missing, invalid, expired,
  expiring within 30 days, without a valid certificate or missing registered
  devices.
- `profiles apply` fixes them after you confirm (`-yes` in CI, `-dry-run` to
  print every step without changing anything).
- `doctor` warns about profiles expiring within 30 days and fails on invalid,
  expired or missing ones.

## Example: Associated Domains for TixNow

TixNow needs universal links for `go.tixnow.com.br`. Declare it in TixNow's
`release.toml`:

```toml
[ios]
bundle_id = "br.com.tixnow.app"
team_id = "ABCDE12345"
signing = "manual"

[ios.capabilities]
associated_domains = ["applinks:go.tixnow.com.br"]
push_notifications = true
```

and add the domain to the app's entitlements file (Xcode: Signing &
Capabilities > Associated Domains), which is where iOS reads it from:

```xml
<key>com.apple.developer.associated-domains</key>
<array>
  <string>applinks:go.tixnow.com.br</string>
</array>
```

Then:

```console
$ mobile-release profiles plan
TixNow (/home/me/apps/tixnow, team ABCDE12345, profiles: ad-hoc, app-store via App Store Connect API)
  br.com.tixnow.app
    ✓ App ID registered
    ✓ Push Notifications
    + enable Associated Domains (ASSOCIATED_DOMAINS)
      · domains go in the entitlements file (com.apple.developer.associated-domains): applinks:go.tixnow.com.br
    ~ ad-hoc profile "TixNow AdHoc": capabilities change (the profile becomes invalid) → recreate
    ~ app-store profile "TixNow AppStore": capabilities change (the profile becomes invalid) → recreate
  3 change(s).

1 app(s): 3 change(s), 0 blocked, 0 with errors.
Nothing was changed. Run `mobile-release profiles apply` to apply.

$ mobile-release profiles apply
...
Apply 3 change(s) to App Store Connect? (y/N)
```

Enabling a capability invalidates every profile of the App ID, so `plan`
already lists them for recreation. Commit the entitlements change with the
`release.toml` change; the next `deploy` signs with the new profiles.

## What release.toml declares

Every key is optional and the schema stays `schema_version = 2`: files
written before this feature keep working unchanged (they declare no
capabilities, and their profile types are derived from the destinations).

```toml
[ios]
bundle_id = "br.com.tixnow.app"
team_id = "ABCDE12345"
signing = "manual"                 # match | automatic | manual

# Capabilities of the app's App ID. Only enabling is managed: capabilities
# enabled in the portal but not listed here are never disabled.
[ios.capabilities]
associated_domains = ["applinks:go.tixnow.com.br"]   # enables ASSOCIATED_DOMAINS
app_groups = ["group.br.com.tixnow"]                 # enables APP_GROUPS (see limitations)
push_notifications = true                            # PUSH_NOTIFICATIONS
sign_in_with_apple = true                            # APPLE_ID_AUTH (primary App ID)
other = ["NFC_TAG_READING"]                          # any App Store Connect capability type

# Other bundle ids shipped inside the app: extensions, App Clip, watch app.
# Each gets its own App ID and the same profile types as the app.
[[ios.extensions]]
bundle_id = "br.com.tixnow.app.notifications"
name = "TixNow Notifications"                        # App ID name when registering it
capabilities = { app_groups = ["group.br.com.tixnow"] }

[[ios.extensions]]
bundle_id = "br.com.tixnow.app.clip"                 # App Clip
capabilities = { associated_domains = ["appclips:go.tixnow.com.br"] }

[ios.provisioning]
# strategy = "api"                 # api | match | none (default: from ios.signing)
# directory = "dist/profiles"      # where profiles are written (default <artifacts>/profiles)
# install = true                   # macOS: also install them for Xcode (default true)

[environments.staging]
destinations = ["firebase"]
ios_profiles = ["development", "ad-hoc"]   # default: derived from destinations

[environments.production]
destinations = ["play", "testflight"]      # → app-store
```

| Key | Meaning |
| --- | --- |
| `ios.capabilities` / `ios.extensions[].capabilities` | capabilities the App ID must have |
| `ios.extensions` | extra bundle ids (extensions, App Clip) managed with the app |
| `environments.<name>.ios_profiles` | `development`, `ad-hoc`, `app-store`. Default: `firebase` → ad-hoc; `testflight`, `app-store`, `artifacts` → app-store (the IPAs `deploy` exports) |
| `ios.team_id` | checked against the App ID prefix: a mismatch means the API key belongs to another team |
| `ios.provisioning.strategy` | who regenerates profiles (below) |

Typos are errors, as everywhere in `release.toml`: `push = true` is rejected,
and `other` only accepts known App Store Connect capability types.

## How profiles are regenerated (and where `deploy` finds them)

The strategy follows `ios.signing`, i.e. how `deploy` signs today:

| `ios.signing` | default strategy | what `apply` does with profiles |
| --- | --- | --- |
| `manual` | `api` | creates them through the API with every valid certificate of the right kind (distribution or development) and, for development/ad-hoc, every enabled registered device; writes them to `dist/profiles/<bundle id>_<type>.mobileprovision`; on macOS installs them in `~/Library/MobileDevice/Provisioning Profiles` and `~/Library/Developer/Xcode/UserData/Provisioning Profiles` (Xcode 16+), removing the copy of the profile it replaced. `gym` picks them up by name. |
| `match` | `match` | runs the embedded `ios profiles` lane: `match(type:, readonly: false, force: true)` for every bundle id of the app, so the match repository stays the source of truth and the next `deploy` (read-only match) gets the new profiles. Needs macOS and the match credentials. |
| `automatic` | `none` | only App IDs and capabilities are changed; Xcode regenerates its managed profiles on the next archive (`-allowProvisioningUpdates` with the API key). `plan` still shows their state. |

Profiles keep their name when recreated, because manual-signing projects
reference them by name (`PROVISIONING_PROFILE_SPECIFIER`). New profiles are
named `<bundle id> AdHoc`, `<bundle id> AppStore` or `<bundle id> Development`.

### Never left without a profile

For each profile, `apply`:

1. writes a local copy of the current profile to `<directory>/.backup/`;
2. creates the new profile first and deletes the old one afterwards;
3. only when App Store Connect refuses a second profile with the same name
   (409), deletes the old one and creates it again.

If creation fails at step 2, nothing was deleted. If it fails after the delete
in step 3, the error names the deleted profile (name, id, UUID) and the backup
file, and running the same `apply` again creates it with the same name (read
from the backup). If enabling a capability fails, the profiles of that App ID
are left alone and the other apps and App IDs continue. Every step is
idempotent: re-running only redoes what is still missing.

Certificates are never created or revoked: when the team has no valid
distribution (or development) certificate the profile is reported as
blocked; create one deliberately (e.g. `MOBILE_RELEASE_MATCH_READONLY=false`
with match, or Xcode).

## Credentials

The command uses the App Store Connect API key already used by `deploy`
(`appstore.key_id`, `appstore.issuer_id`, `appstore.private_key`). `setup`
asks for it when `release.toml` declares capabilities, extensions or
`ios_profiles`, or uses manual signing; `profiles` offers to configure it when
run interactively on one app. The key must be allowed to manage
Certificates, Identifiers & Profiles (an **Admin** key is; check the role of
other keys in App Store Connect > Users and Access > Integrations).

The `.p8` key is read into memory to sign the ES256 token; it is written to a
`0600` temporary file only for the match lane, and never printed. When no
`-env` is given, the shared value is used, falling back to the first
environment that has one. Exec stores (`[stores.x] type = "exec"`) are loaded
when `-env` is given.

## All apps at once

List the projects in a file, one per line (relative to the file, `#` starts a
comment):

```text
# ~/apps/mobile-apps.txt
tixnow
shop
fidelity
delivery
events
wallet
partners
```

```bash
mobile-release profiles plan  -apps ~/apps/mobile-apps.txt
mobile-release profiles apply -apps ~/apps/mobile-apps.txt            # one confirmation for every app
mobile-release profiles apply -apps ~/apps/mobile-apps.txt -dry-run   # every step, nothing changed
```

Project directories can also be passed as arguments
(`mobile-release profiles plan ~/apps/tixnow ~/apps/shop`); without either, the
current project (`-dir`, default `.`) is used. Apps with iOS disabled are
skipped. Apps sharing an API key share one token; rate-limited requests (429)
are retried honouring `Retry-After`.

## CI

A scheduled drift and expiry check (fails when something must change):

```yaml
name: iOS profiles
on:
  schedule: [{ cron: "0 9 * * 1" }]
  workflow_dispatch:
permissions:
  contents: read
jobs:
  check:
    runs-on: ubuntu-latest
    env:
      MOBILE_RELEASE_APP_STORE_KEY_ID: ${{ secrets.MOBILE_RELEASE_APP_STORE_KEY_ID }}
      MOBILE_RELEASE_APP_STORE_ISSUER_ID: ${{ secrets.MOBILE_RELEASE_APP_STORE_ISSUER_ID }}
      MOBILE_RELEASE_APP_STORE_PRIVATE_KEY_BASE64: ${{ secrets.MOBILE_RELEASE_APP_STORE_PRIVATE_KEY_BASE64 }}
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with: { persist-credentials: false }
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with: { go-version: '1.27.3', cache: false }
      - run: go install github.com/lucaswilliameufrasio/mobile-release/v2/cmd/mobile-release@<version>
      - run: mobile-release profiles plan -check
```

To apply in CI, run `mobile-release profiles apply -yes` the same way: on
`ubuntu-latest` for the `api` and `none` strategies (profiles are written to
`dist/profiles`; upload them as an artifact if a later job needs them), on a
macOS runner for `match` (add `MOBILE_RELEASE_MATCH_GIT_URL`,
`MOBILE_RELEASE_MATCH_PASSWORD` and an SSH key for the match repository, as
in the reusable release workflow). Profiles are not secrets, but keep them out
of git: `mobile-release adopt` ignores `dist/`; if you change
`ios.provisioning.directory`, ignore `*.mobileprovision` too.

## Limitations

- The App Store Connect API cannot assign App Group, iCloud container or
  Merchant ids to an App ID: `apply` enables the capability and `plan` reminds
  you to assign the ids in the Apple Developer portal.
- Associated domains are not part of the App ID; they live in the app's
  entitlements file, which this command does not edit.
- Capabilities are only enabled, never disabled.
- Certificates are never created or revoked.
- Only iOS (and universal) App IDs; macOS, tvOS and in-house (enterprise)
  profiles are not managed.
