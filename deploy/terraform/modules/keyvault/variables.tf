variable "name" {
  description = "Key Vault name (3-24 chars, globally unique)."
  type        = string
}

variable "resource_group_name" {
  description = "Resource group name."
  type        = string
}

variable "location" {
  description = "Azure region."
  type        = string
}

variable "tenant_id" {
  description = "Azure AD tenant ID."
  type        = string
}

variable "workload_identity_principal_id" {
  description = "Object/principal ID of the user-assigned identity granted Key Vault Secrets User."
  type        = string
}

variable "deployer_object_id" {
  description = "Object ID of the deployer (e.g. data.azurerm_client_config.current.object_id). Granted Key Vault Secrets Officer. Set to null to skip."
  type        = string
  default     = null
}

variable "tags" {
  description = "Tags."
  type        = map(string)
}
