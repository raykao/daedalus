output "resource_group_name" {
  description = "Name of the resource group containing all Phase 5 resources."
  value       = module.rg.name
}

output "aks_name" {
  description = "AKS cluster name."
  value       = module.aks.name
}

output "aks_resource_group" {
  description = "AKS cluster's resource group name (alias of resource_group_name)."
  value       = module.rg.name
}

output "kubeconfig" {
  description = "Raw kubeconfig for the AKS cluster. Stream to a file: terraform output -raw kubeconfig > kubeconfig."
  value       = module.aks.kube_config_raw
  sensitive   = true
}

output "oidc_issuer_url" {
  description = "AKS OIDC issuer URL."
  value       = module.aks.oidc_issuer_url
}

output "acr_login_server" {
  description = "ACR login server hostname (e.g. acrdaedalustest.azurecr.io)."
  value       = module.acr.login_server
}

output "acr_name" {
  description = "ACR name."
  value       = module.acr.name
}

output "keyvault_uri" {
  description = "Key Vault URI."
  value       = module.keyvault.uri
}

output "keyvault_name" {
  description = "Key Vault name."
  value       = module.keyvault.name
}

output "workload_identity_client_id" {
  description = "Client ID of the workload identity. Annotate K8s ServiceAccount with azure.workload.identity/client-id=<this>."
  value       = module.identity.client_id
}

output "workload_identity_object_id" {
  description = "Object ID of the workload identity."
  value       = module.identity.principal_id
}

output "expires_at" {
  description = "ISO 8601 timestamp at which the cleanup workflow considers this RG eligible for deletion."
  value       = local.expires_at
}

# --- GitHub Actions OIDC identity ---------------------------------------------
# These outputs are wired into the GitHub repo as Actions variables. The
# build-and-publish workflow reads them via ${{ vars.AZURE_CLIENT_ID }} etc.

output "subscription_id" {
  description = "Echo of the input subscription_id. Set as the GitHub repo variable AZURE_SUBSCRIPTION_ID."
  value       = var.subscription_id
}

output "gha_client_id" {
  description = "Client ID of the GitHub Actions managed identity. Set as the GitHub repo variable AZURE_CLIENT_ID."
  value       = module.gha_identity.client_id
}

output "gha_tenant_id" {
  description = "Tenant ID of the GitHub Actions managed identity. Set as the GitHub repo variable AZURE_TENANT_ID."
  value       = module.gha_identity.tenant_id
}

output "gha_principal_id" {
  description = "Object/principal ID of the GitHub Actions managed identity. Useful for verifying role assignments out-of-band."
  value       = module.gha_identity.principal_id
}

output "gha_oidc_subjects" {
  description = "OIDC subjects the GHA federated credential will accept. Cross-check these against the workflow's branch/PR/environment triggers."
  value       = module.gha_identity.subjects
}

output "cleanup_role_assignment_id" {
  description = "Resource ID of the subscription-scoped Contributor role assignment granted to the GHA UAMI for the Phase 5.5 cleanup workflow. Null when var.enable_cleanup_role is false. Useful for `az role assignment show --ids ...` debugging."
  value       = var.enable_cleanup_role ? azurerm_role_assignment.gha_cleanup[0].id : null
}
