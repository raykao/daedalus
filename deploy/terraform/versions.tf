terraform {
  # 1.9.0 introduces cross-variable references in `validation` blocks,
  # which we use to bound `github_oidc_subjects` to the configured
  # github_owner/github_repo (see variables.tf).
  required_version = ">= 1.9.0"

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}
