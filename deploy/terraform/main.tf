# Top-level composition. Modules are wired in dependency order:
#   rg -> aks -> acr (needs AKS kubelet identity)
#   rg -> aks -> identity (needs OIDC issuer)
#   rg -> identity -> keyvault (needs identity principal_id)

module "rg" {
  source   = "./modules/rg"
  name     = local.rg_name
  location = var.region
  tags     = local.common_tags
}

module "aks" {
  source              = "./modules/aks"
  name                = local.aks_name
  resource_group_name = module.rg.name
  location            = module.rg.location
  kubernetes_version  = var.kubernetes_version
  node_count          = var.node_count
  node_vm_size        = var.node_vm_size
  tags                = local.common_tags
}

module "acr" {
  source                         = "./modules/acr"
  name                           = local.acr_name
  resource_group_name            = module.rg.name
  location                       = module.rg.location
  aks_kubelet_identity_object_id = module.aks.kubelet_identity_object_id
  additional_push_principal_ids  = [module.gha_identity.principal_id]
  tags                           = local.common_tags
}

module "gha_identity" {
  source              = "./modules/gha-identity"
  name                = local.gha_identity_name
  resource_group_name = module.rg.name
  location            = module.rg.location
  github_owner        = var.github_owner
  github_repo         = var.github_repo
  subjects            = local.github_oidc_subjects
  tags                = local.common_tags
}

module "identity" {
  source                    = "./modules/identity"
  name                      = local.identity_name
  resource_group_name       = module.rg.name
  location                  = module.rg.location
  oidc_issuer_url           = module.aks.oidc_issuer_url
  service_account_namespace = var.workload_identity_namespace
  service_account_name      = var.workload_identity_service_account
  tags                      = local.common_tags
}

# Phase 5.5: grant the GHA UAMI subscription-scoped Contributor so the
# nightly-cleanup workflow can list and delete TTL-expired resource groups.
#
# Why Contributor (broad) instead of a narrow custom role:
#   - This subscription is dedicated to Phase 5 test infrastructure, with no
#     production workload sharing the blast radius.
#   - Operational simplicity: built-in role, no custom-role lifecycle to
#     manage, no `azurerm_role_definition` resource to drift.
#   - Phase 6 hardening is tracked: replace with a custom role limited to
#     `Microsoft.Resources/subscriptions/resourceGroups/{read,delete}`.
#
# Set `var.enable_cleanup_role = false` to disable; the workflow will then
# fail to delete anything (still authenticates, still lists).
resource "azurerm_role_assignment" "gha_cleanup" {
  count                = var.enable_cleanup_role ? 1 : 0
  scope                = data.azurerm_subscription.current.id
  role_definition_name = "Contributor"
  principal_id         = module.gha_identity.principal_id
  description          = "Phase 5.5 TTL cleanup workflow - list and delete tagged resource groups"
}

module "keyvault" {
  source                         = "./modules/keyvault"
  name                           = local.kv_name
  resource_group_name            = module.rg.name
  location                       = module.rg.location
  tenant_id                      = data.azurerm_client_config.current.tenant_id
  workload_identity_principal_id = module.identity.principal_id
  deployer_object_id             = data.azurerm_client_config.current.object_id
  tags                           = local.common_tags
}
