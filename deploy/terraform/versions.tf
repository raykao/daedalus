terraform {
  required_version = ">= 1.7"

  # ---------------------------------------------------------------------------
  # Remote state backend (recommended for shared/team use).
  # Uncomment and fill in values before first `terraform init`.
  # Create the storage account and container manually before using this.
  # ---------------------------------------------------------------------------
  # backend "azurerm" {
  #   resource_group_name  = "rg-tfstate"
  #   storage_account_name = "stterraformstate"
  #   container_name       = "daedalus-tfstate"
  #   key                  = "phase4-test-cluster.tfstate"
  # }

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 3.110"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.31"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.14"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}
