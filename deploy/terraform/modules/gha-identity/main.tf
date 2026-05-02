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
  # resource name. Each name is suffixed with a 6-char sha1 of the full
  # subject so it is provably collision-free: two subjects whose
  # human-readable parts collapse to the same string after `/`->`-`
  # substitution (e.g. `refs/heads/feat-foo` and `refs/heads/feat/foo` both
  # reduce to `feat-foo`) would otherwise hit Azure with a duplicate FIC
  # name mid-apply. Subjects are unique by construction (`for_each` keys),
  # so the hash suffix is enough to disambiguate.
  #
  # Examples:
  #   repo:raykao/daedalus:ref:refs/heads/main      -> "main-3f9a2b"
  #   repo:raykao/daedalus:pull_request             -> "pr-7c1e44"
  #   repo:raykao/daedalus:environment:test         -> "env-test-a1b2c3"
  fic_short_names = {
    for s in var.subjects :
    s => format("%s-%s", (
      can(regex(":environment:([A-Za-z0-9_-]+)$", s)) ?
      "env-${regex(":environment:([A-Za-z0-9_-]+)$", s)[0]}" :
      can(regex(":pull_request$", s)) ?
      "pr" :
      can(regex(":ref:refs/heads/([A-Za-z0-9_./-]+)$", s)) ?
      replace(regex(":ref:refs/heads/([A-Za-z0-9_./-]+)$", s)[0], "/", "-") :
      can(regex(":ref:refs/tags/([A-Za-z0-9_./-]+)$", s)) ?
      "tag-${replace(regex(":ref:refs/tags/([A-Za-z0-9_./-]+)$", s)[0], "/", "-")}" :
      substr(sha1(s), 0, 8)
    ), substr(sha1(s), 0, 6))
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
