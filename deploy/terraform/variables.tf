variable "subscription_id" {
  description = "Azure subscription ID to deploy into. Must be a UUID."
  type        = string

  validation {
    condition     = can(regex("^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$", var.subscription_id))
    error_message = "subscription_id must be a UUID like 00000000-0000-0000-0000-000000000000."
  }
}

variable "env_name" {
  description = "Short environment name (e.g. test, dev). Lowercase alphanumerics, 2-10 chars."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9]{2,10}$", var.env_name))
    error_message = "env_name must be 2-10 lowercase alphanumeric characters."
  }
}

variable "region" {
  description = "Azure region to deploy into. Restricted to a small allowlist for cost predictability."
  type        = string
  default     = "eastus"

  validation {
    condition     = contains(["eastus", "eastus2", "westus2", "centralus", "canadacentral"], var.region)
    error_message = "region must be one of: eastus, eastus2, westus2, centralus, canadacentral."
  }
}

variable "name_prefix" {
  description = "Prefix used in all resource names. Lowercase alphanumerics, 3-12 chars. Combined with env_name, the body of the Key Vault name is truncated to 14 chars (so a 6-char uniqueness suffix always survives Azure's 24-char KV cap)."
  type        = string
  default     = "daedalus"

  validation {
    condition     = can(regex("^[a-z0-9]{3,12}$", var.name_prefix))
    error_message = "name_prefix must be 3-12 lowercase alphanumeric characters."
  }
}

variable "kubernetes_version" {
  description = "AKS Kubernetes version. Set to null to let Azure pick the default supported version."
  type        = string
  default     = null
}

variable "node_count" {
  description = "Default node pool node count."
  type        = number
  default     = 2

  validation {
    condition     = var.node_count >= 1 && var.node_count <= 10
    error_message = "node_count must be between 1 and 10 for the test cluster."
  }
}

variable "node_vm_size" {
  description = "VM size for the AKS default node pool."
  type        = string
  default     = "Standard_D2s_v5"
}

variable "ttl_hours" {
  description = "How many hours after apply the resource group should be considered expired by the cleanup workflow (Phase 5.5)."
  type        = number
  default     = 4

  validation {
    condition     = var.ttl_hours > 0 && var.ttl_hours <= 24
    error_message = "ttl_hours must be > 0 and <= 24."
  }
}

variable "workload_identity_namespace" {
  description = "Kubernetes namespace where the daedalus workload runs. Used in the federated credential subject."
  type        = string
  default     = "daedalus"
}

variable "workload_identity_service_account" {
  description = "Kubernetes ServiceAccount name for the workload identity. Used in the federated credential subject."
  type        = string
  default     = "daedalus-proxy"
}

variable "tags" {
  description = "Common tags applied to every resource."
  type        = map(string)
  default = {
    project    = "daedalus"
    phase      = "5"
    managed_by = "terraform"
  }
}
