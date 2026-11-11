# mobile-release

CLI em Go que builda e distribui apps **Flutter**, **Expo**, **Kotlin
Multiplatform** e **nativos** (Gradle + Xcode, inclusive projetos gerados pelo
[jirau](https://github.com/lucaswilliameufrasio/jirau)). Cada app tem só um
`release.toml` sem segredos; a CLI pergunta o que falta, guarda as credenciais
uma vez no seu cofre e roda o mesmo fluxo localmente ou no CI.

- **Guiada:** `init` detecta o projeto, `setup` pede cada credencial que falta
  (com o que é e onde obter), `doctor` mostra o que está pronto, `deploy`
  confirma antes de publicar.
- **Sem Fastfile no app:** as lanes do fastlane vêm embutidas no binário, com
  versões fixadas (`Gemfile.lock`).
- **Credenciais nunca aparecem:** valores trafegam por ambiente ou stdin,
  arquivos viram temporários `0600`, e os resumos mostram só nome → fonte.

## Instalação

```bash
go install github.com/lucaswilliameufrasio/mobile-release/cmd/mobile-release@latest
```

Requer Ruby ≥ 3.2 com Bundler (as gems do fastlane ficam no cache do usuário)
e as ferramentas do seu projeto (Flutter, Node, JDK, Xcode). `mobile-release
doctor` lista o que falta e como instalar.

## Início rápido

```bash
cd meu-app
mobile-release init      # detecta tipo, ids e arquivos .env; pergunta ambientes e destinos
mobile-release setup     # pede as credenciais que faltam e grava no cofre escolhido
mobile-release doctor    # ferramentas, config e credenciais por ambiente
mobile-release deploy    # escolhe ambiente e plataforma, resume, confirma e publica
```

`deploy -dry-run` mostra todos os comandos sem buildar nem publicar.
`mobile-release qa` gera APK e build de simulador sem assinatura de release.

## release.toml

```toml
schema_version = 2

[project]
type = "flutter"            # flutter | expo | kmp | native
# preset = "jirau"          # native gerado pelo jirau (detectado pelo jirau.toml)

[app]
name = "Shop"

[android]
package_name = "com.example.shop"
# signing = "project"       # padrão "injected": a CLI injeta o keystore no Gradle

[ios]
bundle_id = "com.example.shop"
team_id = "ABCDE12345"
signing = "match"           # automatic (chave do App Store Connect) | match | manual

[environments.staging]
env_file = ".env"
defines = { APP_COMMIT = "{commit}" }
destinations = ["firebase"]

[environments.staging.firebase]
android_app_id = "1:123:android:abc"
ios_app_id = "1:123:ios:def"
groups = ["internal-testers"]

[environments.production]
env_file = ".env.production"
defines = { APP_VARIANT = "production" }
build_number = "store"
destinations = ["play", "testflight"]
```

| Campo | Valores |
| --- | --- |
| `destinations` | `firebase` (APK + IPA ad-hoc), `play` (AAB), `testflight`, `app-store` (envio para revisão), `ota` (Expo), `artifacts` (só builda em `dist/`) |
| `build_number` | `timestamp` (UTC `yyDDDHHMM`, padrão), `store` (último da loja + 1), `project` (mantém o do projeto) ou um inteiro |
| `env_file` | Flutter: `--dart-define-from-file`; demais: variáveis de ambiente do build (ex.: `EXPO_PUBLIC_*`) |
| `defines` | Flutter: `--dart-define`; demais: ambiente. Aceita `{commit}`, `{build_number}` e `{env}` |
| `[prepare]` | `all`, `android`, `ios`: comandos de shell antes dos builds |
| `confirm` | por padrão, ambientes com `play`, `testflight` ou `app-store` exigem digitar o nome do ambiente |

Campos desconhecidos geram erro (typos não viram defaults silenciosos) e a
validação lista todos os problemas de uma vez. Arquivos `schema_version = 1`
continuam funcionando como um ambiente `internal`. Veja [`examples/`](examples/)
para Flutter, Expo, KMP, nativo e jirau.

### Como cada tipo builda

| Tipo | Android | iOS (antes do archive) |
| --- | --- | --- |
| Flutter | `flutter build apk/appbundle` com defines, ofuscação e `--build-number` | `flutter build ios --config-only` |
| Expo | instala dependências (pnpm/yarn/bun/npm pelo lockfile), `expo prebuild`, Gradle | — |
| KMP | `apk_task`/`aab_task` | projeto `iosApp` |
| Nativo | `./gradlew assembleRelease/bundleRelease` em `android/` | `xcodegen` se houver spec |
| jirau | Gradle (o núcleo Rust é compilado no `preBuild`) | `scripts/build-ios-ffi.sh release` + `xcodegen generate` |

O archive e o export do IPA são comuns a todos (fastlane `gym`), então todos os
tipos têm as mesmas opções de assinatura. O número de build chega ao Android
por `android.injected.version.code` e ao iOS por `CURRENT_PROJECT_VERSION`
(Flutter usa os próprios mecanismos). A assinatura Android de release usa as
propriedades injetadas do Android Gradle Plugin via `ORG_GRADLE_PROJECT_*`:
o app não precisa de `key.properties`, e senhas não aparecem em argumentos.

## Credenciais

A CLI deduz quais credenciais cada deploy precisa a partir dos destinos, da
assinatura e da estratégia de número de build: você nunca precisa saber nomes
de variáveis. Valores podem valer para todos os ambientes (ex.: keystore) ou só
para um (ex.: conta de serviço do Firebase de staging).

Cada credencial é procurada nos cofres em ordem, e vale o primeiro que tiver:

1. `keychain` — Keychain do macOS (serviço `mobile-release`).
2. Cofres personalizados (abaixo), em ordem alfabética.
3. `bitwarden` — Bitwarden CLI. No `setup` você escolhe o item (só nomes são
   exibidos) e a escolha fica lembrada pelo id do item.
4. `env` — variáveis `MOBILE_RELEASE_*` (CI).

### Configuração pessoal

Fica em `~/.config/mobile-release/config.toml` (Linux) ou
`~/Library/Application Support/mobile-release/config.toml` (macOS), nunca no
projeto:

```toml
# Opcional: ordem explícita
order = ["keychain", "meu-cofre", "bitwarden", "env"]
default_store = "keychain"

# Uma ferramenta que roda um comando com os segredos no ambiente
# (formato "<ferramenta> run -- <comando>"). A CLI se executa dentro dela.
[stores.meu-cofre]
type = "exec"
command = ["meu-cofre", "run", "--env", "{env}", "--"]
env_names = { production = "prd" }          # nome do ambiente na ferramenta
# vars = { "android.keystore" = "ANDROID_KEYSTORE_BASE64" }  # se os nomes diferirem

# Ou uma ferramenta de leitura/escrita por chave (1Password, pass, ...)
[stores.op]
type = "command"
get = ["op", "read", "op://Private/{key}"]  # imprime o valor
# set = [...]                                # recebe o valor no stdin
```

Placeholders: `{app}`, `{env}`, `{id}` e `{key}` (`app/env/id`). Arquivos são
guardados em base64.

## CI

O workflow reutilizável instala a CLI e roda `deploy` sem prompts (`-yes`):

```yaml
jobs:
  release:
    uses: lucaswilliameufrasio/mobile-release/.github/workflows/reusable-release.yml@v2.0.0
    with:
      environment: staging
      platform: android
      runner: ubuntu-latest           # iOS precisa de macOS
      flutter-version-file: pubspec.yaml
    secrets: inherit
```

Secrets aceitos: `MOBILE_RELEASE_ANDROID_KEYSTORE_BASE64`,
`MOBILE_RELEASE_ANDROID_KEYSTORE_PASSWORD`, `MOBILE_RELEASE_ANDROID_KEY_ALIAS`,
`MOBILE_RELEASE_ANDROID_KEY_PASSWORD`, `MOBILE_RELEASE_GOOGLE_PLAY_JSON_BASE64`,
`MOBILE_RELEASE_FIREBASE_SERVICE_ACCOUNT_BASE64`,
`MOBILE_RELEASE_APP_STORE_KEY_ID`, `MOBILE_RELEASE_APP_STORE_ISSUER_ID`,
`MOBILE_RELEASE_APP_STORE_PRIVATE_KEY_BASE64`, `MOBILE_RELEASE_MATCH_GIT_URL`,
`MOBILE_RELEASE_MATCH_PASSWORD`, `MOBILE_RELEASE_MATCH_SSH_KEY` e
`MOBILE_RELEASE_EXPO_TOKEN`. Faltando algo, o erro lista exatamente quais.

## Segurança

- Nenhum segredo vai no `release.toml`, em argumentos de processo ou nos logs;
  os comandos impressos mostram só os **nomes** das variáveis passadas.
- Comandos de OTA recebem apenas o token do Expo (modo `eas`), nunca chaves de
  assinatura ou de loja.
- O match roda só leitura; para criar certificados, rode com
  `MOBILE_RELEASE_MATCH_READONLY=false` conscientemente.
- Não commite `.p8`, `.p12`, perfis, contas de serviço ou keystores.

## Desenvolvimento

```bash
mise install
make test        # testes unitários e de integração (runner falso)
make lint
```

Os testes de integração validam a sequência completa de comandos de cada tipo
de projeto sem SDKs nativos instalados.
