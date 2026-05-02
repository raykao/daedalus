# User-assigned managed identity for GitHub Actions, plus one federated
# credential per supported GHA OIDC subject. Tokens minted by GitHub Actions
# (issuer https://token.actions.githubusercontent.com) for the configured
# repo + ref/PR/environment patterns are exchanged for Entra ID tokens bound
# to this identity.
#
# This identity is the principal that GitHub Actions uses to push images to
# ACR (and any other Azure resource we explicitly grant it access to). Role
# assignments are NOT created in this module - they are granted by the
# resource modules (e.g. acr) via additional_push_principal_ids inputs, so
# the blast radius of this identity is visible in those modules' diffs.

locals {
  # Build a deterministic short name per subject so each
  # azurerm_federated_identity_credential gets a stable, human-meaningful
  # resource name. Only the discriminating part of the subject is used.
  #
  # Examples:
  #   repo:raykao/daedalus:ref:refs/heads/main      -> "main"
  #   repo:raykao/daedalus:pull_request             -> "pr"
  #   repo:raykao/daedalus:environment:test         -> "env-test"
  fic_short_names = {
    for s in var.subjects :
    s => (
      can(regex(":environment:([A-Za-z0-9_-]+)$", s)) ?
      "env-${regex(":environment:([A-Za-z0-9_-]+)$", s)[0]}" :
      can(regex(":pull_request$", s)) ?
      "pr" :
      can(regex(":ref:refs/heads/([A-Za-z0-9_./-]+)$", s)) ?
      replace(regex(":ref:refs/heads/([A-Za-z0-9_./-]+)$", s)[0], "/", "-") :
      can(regex(":ref:refs/tags/([A-Za-z0-9_./-]+)$", s)) ?
      "tag-${replace(regex(":ref:refs/tags/([A-Za-z0-9_./-]+)$", s)[0], "/", "-")}" :
      substr(sha1(s), 0, 8)
    )
  }
}

resource "azurerm_user_assigned_identity" "this" {
  name                = var.name
  resource_group_name = var.resource_group_name
  location            = var.location
  tags                = var.tags
}

resource "azurerm_federated_identity_credential" "this" {
  for_each = local.fic_short_names

  name                = "gha-fic-${each.value}"
  resource_group_name = var.resource_group_name
  parent_id           = azurerm_user_assigned_identity.this.id

  audience = ["api://AzureADTokenExchange"]
  issuer   = "https://token.actions.githubusercontent.com"
  subject  = each.key
}
