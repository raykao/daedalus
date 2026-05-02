variable "name" {
  description = "ACR name. 5-50 alphanumerics, globally unique."
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

variable "aks_kubelet_identity_object_id" {
  description = "Object ID of the AKS kubelet identity to grant AcrPull."
  type        = string
}

variable "additional_push_principal_ids" {
  description = "Additional principal object IDs to grant AcrPush on this registry. Used for non-AKS publishers such as the GitHub Actions OIDC identity."
  type        = list(string)
  default     = []
}

variable "tags" {
  description = "Tags."
  type        = map(string)
}
