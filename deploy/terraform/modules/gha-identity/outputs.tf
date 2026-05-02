output "id" {
  description = "User-assigned identity resource ID."
  value       = azurerm_user_assigned_identity.this.id
}

output "client_id" {
  description = "Client (application) ID. Set as the GitHub repo variable AZURE_CLIENT_ID for azure/login@v2."
  value       = azurerm_user_assigned_identity.this.client_id
}

output "principal_id" {
  description = "Object/principal ID. Use as the principal in role assignments (e.g. AcrPush)."
  value       = azurerm_user_assigned_identity.this.principal_id
}

output "tenant_id" {
  description = "Tenant ID of the identity. Set as the GitHub repo variable AZURE_TENANT_ID."
  value       = azurerm_user_assigned_identity.this.tenant_id
}

output "subjects" {
  description = "OIDC subjects registered on this identity's federated credentials."
  value       = var.subjects
}
