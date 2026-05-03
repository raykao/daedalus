provider "azurerm" {
  subscription_id = var.subscription_id

  features {
    key_vault {
      # Test environment: allow Terraform destroy to actually remove KVs without
      # leaving soft-deleted shells around. Soft-delete is still on (Azure
      # requires it), but purge protection is disabled (see modules/keyvault).
      purge_soft_delete_on_destroy          = true
      purge_soft_deleted_secrets_on_destroy = true
      recover_soft_deleted_key_vaults       = true
      recover_soft_deleted_secrets          = true
    }
    resource_group {
      # Allow destroy even if resources still exist in the RG. Useful when the
      # TTL cleanup script (Phase 5.5) reaps the whole RG.
      prevent_deletion_if_contains_resources = false
    }
  }
}

# Surfaces the deployer's tenant + object_id so we can grant them KV Secrets
# Officer for out-of-band secret writes.
data "azurerm_client_config" "current" {}

# Phase 5.5: needed for subscription-scoped role assignments granted to the
# GitHub Actions cleanup UAMI. See `azurerm_role_assignment.gha_cleanup` in
# main.tf.
data "azurerm_subscription" "current" {}
