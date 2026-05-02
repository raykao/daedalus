# Resource group with TTL tags. The `auto-destroy=true` and `expires-at=<ISO8601>`
# tags form the contract consumed by the Phase 5.5 cleanup workflow.

resource "azurerm_resource_group" "this" {
  name     = var.name
  location = var.location
  tags     = var.tags
}
