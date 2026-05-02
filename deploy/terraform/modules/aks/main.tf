# AKS cluster with OIDC issuer + workload identity enabled. System-assigned
# identity is sufficient here; workload identities for app pods are wired
# separately via the identity module + federated credentials.

resource "azurerm_kubernetes_cluster" "this" {
  name                = var.name
  resource_group_name = var.resource_group_name
  location            = var.location
  dns_prefix          = var.name
  kubernetes_version  = var.kubernetes_version

  oidc_issuer_enabled       = true
  workload_identity_enabled = true

  default_node_pool {
    name                        = "system"
    vm_size                     = var.node_vm_size
    node_count                  = var.node_count
    os_disk_size_gb             = 75
    os_disk_type                = "Managed" # Ephemeral not supported on v4/v5 SKUs (no cache disk)
    temporary_name_for_rotation = "tmpsys"

    upgrade_settings {
      max_surge = "1"
    }
  }

  identity {
    type = "SystemAssigned"
  }

  network_profile {
    network_plugin      = "azure"
    network_plugin_mode = "overlay"
  }

  tags = var.tags
}
