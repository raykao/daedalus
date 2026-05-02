# Remote state backend.
#
# Bootstrap: run bootstrap/bootstrap.sh once to create the storage account, then
# uncomment the block below (replacing the storage account name with the value
# the bootstrap script prints) and run `terraform init -migrate-state`.
#
# terraform {
#   backend "azurerm" {
#     resource_group_name  = "rg-daedalus-tfstate"
#     storage_account_name = "stdaedalustfstateXXXXXX"
#     container_name       = "tfstate"
#     key                  = "phase5.terraform.tfstate"
#     use_azuread_auth     = true
#   }
# }
