# Key Vault, RBAC mode. Purge protection intentionally OFF for the test
# environment so re-applies / destroys don't leave undeletable shells around.
# Soft-delete cannot be disabled (Azure constraint), but retention is set to
# the minimum 7 days.

resource "azurerm_key_vault" "this" {
  name                = var.name
  resource_group_name = var.resource_group_name
  location            = var.location

  tenant_id                  = var.tenant_id
  sku_name                   = "standard"
  rbac_authorization_enabled = true
  purge_protection_enabled   = false
  soft_delete_retention_days = 7

  tags = var.tags
}

# Workload identity reads secrets from this vault.
resource "azurerm_role_assignment" "workload_identity_secrets_user" {
  scope                = azurerm_key_vault.this.id
  role_definition_name = "Key Vault Secrets User"
  principal_id         = var.workload_identity_principal_id
}

# Whoever runs `terraform apply` (or pushes secrets via az cli) needs write
# access. Granted at the KV scope so they can manage `github-token` etc. out
# of band. Skip if no deployer object_id is supplied.
resource "azurerm_role_assignment" "deployer_secrets_officer" {
  count                = var.deployer_object_id == null ? 0 : 1
  scope                = azurerm_key_vault.this.id
  role_definition_name = "Key Vault Secrets Officer"
  principal_id         = var.deployer_object_id
}
