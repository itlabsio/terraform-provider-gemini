resource "gemini_source_instance" "core" {
  name               = "prod-core"
  project            = gemini_project.billing.name
  host               = "c-xxxx.rw.mdb.yandexcloud.net"
  port               = 6432
  ssl_mode           = "verify-full"
  discovery_db       = "app"
  excluded_databases = ["tmp"]

  auth_type                 = "plain"
  plain_username            = "backup"
  plain_password_wo         = var.core_backup_password
  plain_password_wo_version = 1
}

resource "gemini_source_instance" "legacy" {
  name       = "legacy"
  project    = gemini_project.billing.name
  host       = "pg.internal"
  auth_type  = "vault"
  vault_path = "secret/data/db/legacy"
}
