resource "random_string" "suffix" {
  length  = 6
  upper   = false
  lower   = true
  numeric = true
  special = false
}

resource "azurerm_resource_group" "main" {
  name     = var.resource_group_name
  location = var.location
  tags     = var.tags
}

locals {
  acr_name = var.acr_name != "" ? var.acr_name : "acrdaedalus${random_string.suffix.result}"
}
