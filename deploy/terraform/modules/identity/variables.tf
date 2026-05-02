variable "name" {
  description = "User-assigned managed identity name."
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

variable "oidc_issuer_url" {
  description = "AKS OIDC issuer URL (from aks module output)."
  type        = string
}

variable "service_account_namespace" {
  description = "Kubernetes namespace of the ServiceAccount that will assume this identity."
  type        = string
}

variable "service_account_name" {
  description = "Kubernetes ServiceAccount name."
  type        = string
}

variable "tags" {
  description = "Tags."
  type        = map(string)
}
