# Preview local de APK Android

O preview é uma sessão temporária de distribuição, não uma publicação nem
atualização silenciosa. O operador escolhe um APK existente ou o build de QA
Android e informa explicitamente o endereço local onde o servidor deve escutar.
O endereço precisa ser alcançável pelo aparelho.

O servidor oferece somente o APK selecionado por um caminho aleatório, termina
automaticamente no prazo escolhido ou ao receber interrupção e não registra
requisições. O QR contém um link de acesso: qualquer pessoa que o obtenha pode
baixar o APK enquanto a sessão estiver ativa. HTTP não oferece confidencialidade
contra observadores da rede; usar apenas rede confiável e nunca disponibilizar
APKs contendo credenciais. Não servir diretórios, arquivos arbitrários nem
abrir acesso público automaticamente.

O build continua sendo responsabilidade dos providers existentes. A seleção de
flavor e perfil é a configurada pelo projeto; não se presume que todas as
variantes existam ou estejam assinadas. Hot reload Flutter não faz parte deste
fluxo. Uma engine hospedeira Flutter exige prova de conceito independente.

Para Flutter, o preview pode escolher explicitamente `debug`, `profile` ou
`release` sem alterar o comportamento de `qa` ou `deploy`. Para Gradle nativo,
a tarefa e o caminho do APK permanecem configurados no projeto. Uma variante
não contemplada pela configuração pode ser construída fora da ferramenta e
compartilhada com `-apk`.
