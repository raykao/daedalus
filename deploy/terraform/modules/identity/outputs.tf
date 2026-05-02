output "id" {
  description = "User-assigned identity resource ID."
  value       = azurerm_user_assigned_identity.this.id
}

output "client_id" {
  description = "Client (application) ID. Annotate the K8s SA with azure.workload.identity/client-id=<this>."
  value       = azurerm_user_assigned_identity.this.client_id
}

output "principal_id" {
  description = "Object/principal ID. Use as the principal in role assignments (e.g. Key Vault Secrets User)."
  value       = azurerm_user_assigned_identity.this.principal_id
}

output "tenant_id" {
  description = "Tenant ID of the identity."
  value       = azurerm_user_assigned_identity.this.tenant_id
}
