# terraform-provider-gemini

Terraform-провайдер для [Gemini](https://github.com/itlabsio/gemini): бэкапа и
восстановления PostgreSQL между контурами.

Ресурсы:

- `gemini_project` — проект RBAC (имя = группа Keycloak верхнего уровня);
- `gemini_source_instance` — source-инстанс PostgreSQL на source-инстансе Gemini.

Документация — в [docs/](docs/), примеры — в [examples/](examples/).

## Доступ

Провайдер работает от имени service account Keycloak:

1. Клиент, например `gemini-terraform`, с включёнными *Service accounts roles*.
2. Service-account пользователю выдаётся client-роль `superuser` клиента Gemini
   (`gemini`): создавать проекты и инстансы может только superuser.
3. В токене `aud` должен содержать клиент Gemini. Keycloak добавляет его сам для
   клиентов, чьи роли попали в токен; иначе нужен Audience mapper.

## Разработка

```sh
go test ./...                    # нужен terraform в PATH: тесты гоняют его против фейкового API
go tool tfplugindocs generate    # перегенерировать docs/ из схемы и examples/
```

Локальная сборка вместо Registry — через `dev_overrides` в `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "itlabsio/gemini" = "/path/to/go/bin"
  }
  direct {}
}
```

## Релиз

Тег `vX.Y.Z` → GitHub Actions собирает провайдер goreleaser'ом, подписывает
SHA256SUMS GPG-ключом и создаёт GitHub Release, откуда его забирает Terraform
Registry. Нужны секреты `GPG_PRIVATE_KEY` и `PASSPHRASE`.
