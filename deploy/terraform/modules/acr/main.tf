# Azure Container Registry. Standard SKU is enough for a single-region test
# cluster. admin_enabled is intentionally false - AKS pulls via the kubelet
# identity's AcrPull role, not via username/password.

resource "azurerm_container_registry" "this" {
  name                = var.name
  resource_group_name = var.resource_group_name
  location            = var.location
  sku                 = "Standard"
  admin_enabled       = false
  tags                = var.tags
}

resource "azurerm_role_assignment" "aks_acr_pull" {
  scope                = azurerm_container_registry.this.id
  role_definition_name = "AcrPull"
  principal_id         = var.aks_kubelet_identity_object_id
}

# Optional AcrPush grants for non-AKS principals (e.g. the GitHub Actions
# managed identity that publishes images). Keyed by principal_id so the plan
# diff is stable regardless of list ordering.
resource "azurerm_role_assignment" "additional_push" {
  for_each = toset(var.additional_push_principal_ids)

  scope                = azurerm_container_registry.this.id
  role_definition_name = "AcrPush"
  principal_id         = each.value
}
