terraform {
  required_providers {
    gemini = {
      source = "itlabsio/gemini"
    }
  }
}

# Service account Keycloak с client-ролью gemini → superuser.
provider "gemini" {
  url             = "https://gemini.example.com"
  oidc_issuer_url = "https://keycloak.example.com/realms/example"
  client_id       = "gemini-terraform"
  client_secret   = var.gemini_client_secret
}
