variable "name" {
  description = "User-assigned managed identity name for the GitHub Actions OIDC principal."
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

variable "github_owner" {
  description = "GitHub organization or user that owns the repo. Used only for documentation/validation; the actual subjects passed to the federated credential come from var.subjects."
  type        = string
}

variable "github_repo" {
  description = "GitHub repository name (no owner). Used only for documentation/validation; the actual subjects come from var.subjects."
  type        = string
}

variable "subjects" {
  description = <<-EOT
    Federated credential subjects to register. Each string is matched verbatim
    against the OIDC token's `sub` claim. Default covers main-branch pushes
    and pull_request runs from the configured repo. To add an environment,
    append `repo:<owner>/<repo>:environment:<env>`.

    Reference: https://docs.github.com/en/actions/deployment/security-hardening-your-deployments/about-security-hardening-with-openid-connect#example-subject-claims
  EOT
  type        = list(string)

  validation {
    condition     = length(var.subjects) > 0
    error_message = "subjects must contain at least one OIDC subject string."
  }

  # Defence-in-depth: even if a future caller forgets the equivalent
  # validation in their root config, this module must refuse subjects from
  # other repos. Cross-repo OIDC trust is an explicit decision, not a typo.
  validation {
    condition = alltrue([
      for s in var.subjects :
      can(regex("^repo:${var.github_owner}/${var.github_repo}:", s))
    ])
    error_message = "Every subject must start with \"repo:<github_owner>/<github_repo>:\". Cross-repo OIDC trust must be an explicit, separate decision (instantiate the module again with the other repo's github_owner/github_repo)."
  }
}

variable "tags" {
  description = "Tags."
  type        = map(string)
}
