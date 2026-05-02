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
  tags                           = local.common_tags
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
