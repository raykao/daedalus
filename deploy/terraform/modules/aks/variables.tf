variable "name" {
  description = "AKS cluster name."
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

variable "kubernetes_version" {
  description = "Kubernetes version. null = let Azure pick."
  type        = string
  default     = null
}

variable "node_count" {
  description = "Default node pool node count."
  type        = number
}

variable "node_vm_size" {
  description = "Default node pool VM size. Must support an ephemeral OS disk of >= 75 GiB. D-series v3+ at D2/D4 sizes all qualify."
  type        = string
}

variable "tags" {
  description = "Tags."
  type        = map(string)
}
