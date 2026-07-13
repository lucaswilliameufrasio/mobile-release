# mobile-release

CLI reutilizável em Go para builds e distribuição de apps Expo/prebuild, Flutter e Kotlin Multiplatform. O projeto centraliza a orquestração; cada app precisa apenas de `release.toml` e, opcionalmente, um workflow fino.

## Configuração

Precedência: flags da CLI > variáveis `MOBILE_RELEASE_*` > `release.toml` > detecção/defaults. Secrets podem ser caminhos (`*_PATH`) ou conteúdo Base64 (`*_BASE64`), ideal para GitHub Actions. Arquivos Base64 são materializados com permissão `0600` em diretórios temporários.

## Comandos

```bash
go install ./cmd/mobile-release
mobile-release doctor
mobile-release qa
mobile-release -platform android internal
mobile-release -platform all internal
```

`qa` constrói APK e `.app` de simulador. `internal` constrói AAB e/ou IPA e chama as lanes compartilhadas do Fastlane para Play Internal Testing e TestFlight.

## Providers

- Expo: `pnpm install`, `expo prebuild`, Gradle e `xcodebuild`.
- Flutter: `flutter pub get`, `flutter build apk/appbundle/ios/ipa`.
- KMP: tasks Gradle configuráveis e projeto Xcode configurável.

## GitHub Actions

O workflow em `.github/workflows/reusable-release.yml` pode ficar neste repositório central e ser chamado pelos projetos. Em GitHub-hosted, use Linux para Android isolado e macOS para iOS ou `all`. Em self-hosted, as mesmas env vars funcionam, sem depender de arquivos de perfil da máquina.

## Testes

```bash
make test
make lint
```

Os testes unitários cobrem parsing, precedência de env, validação, detecção de projeto e materialização de secrets. Os testes de integração usam um runner falso para validar os comandos completos de Expo, Flutter e KMP sem exigir SDKs nativos instalados.

## Segurança

Não commite `.p8`, `.p12`, provisioning profiles, service accounts ou keystores. Em CI, use secrets Base64; localmente, prefira caminhos fora do repositório. A CLI nunca deve imprimir conteúdo de secrets.

## OTA

A configuração de OTA está presente no exemplo Expo. O servidor deve implementar o Expo Updates Protocol; a publicação concreta depende do contrato HTTP do seu backend e deve ser conectada no módulo OTA.
