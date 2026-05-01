variable "location" {
  description = "Azure region for all resources"
  type        = string
  default     = "eastus2"
}

variable "resource_group_name" {
  description = "Name of the Azure Resource Group for the test cluster"
  type        = string
  default     = "rg-daedalus-test"
}

variable "cluster_name" {
  description = "Name of the AKS cluster"
  type        = string
  default     = "aks-daedalus-test"
}

variable "kubernetes_version" {
  description = "Kubernetes version for the AKS cluster"
  type        = string
  # Check AKS supported versions before deploying: https://learn.microsoft.com/azure/aks/supported-kubernetes-versions
  default = "1.30"
}

variable "node_vm_size" {
  description = "VM size for AKS default node pool"
  type        = string
  default     = "Standard_D2s_v3"
}

variable "node_count" {
  description = "Number of nodes in the AKS default node pool"
  type        = number
  default     = 2
}

variable "acr_name" {
  description = "Azure Container Registry name. If empty, auto-generated as acrdaedalus<random_suffix>"
  type        = string
  default     = ""

  validation {
    condition     = var.acr_name == "" || can(regex("^[a-zA-Z0-9]{5,50}$", var.acr_name))
    error_message = "acr_name must be 5-50 alphanumeric characters (no hyphens or underscores), or empty for auto-generation."
  }
}

variable "keda_chart_version" {
  description = "Helm chart version for KEDA"
  type        = string
  default     = "2.15.1"
}

variable "tags" {
  description = "Tags to apply to all resources"
  type        = map(string)
  default = {
    project     = "daedalus"
    environment = "test"
    managed_by  = "terraform"
  }
}
