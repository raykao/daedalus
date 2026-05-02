output "id" {
  description = "Key Vault resource ID."
  value       = azurerm_key_vault.this.id
}

output "name" {
  description = "Key Vault name."
  value       = azurerm_key_vault.this.name
}

output "uri" {
  description = "Key Vault URI (e.g. https://<name>.vault.azure.net/)."
  value       = azurerm_key_vault.this.vault_uri
}
