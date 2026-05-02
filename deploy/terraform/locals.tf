locals {
  # NOTE: timestamp() is evaluated at *plan* time and re-runs on every plan/apply.
  # That means `expires-at` drifts forward each time you run `terraform apply`,
  # which is fine for this test environment because every apply is an explicit
  # signal that the cluster is in active use. The Phase 5.5 cleanup workflow
  # treats the tag as a soft-deadline; if you want a hard deadline, set ttl_hours
  # smaller and avoid running spurious applies.
  expires_at = timeadd(timestamp(), "${var.ttl_hours}h0m0s")

  common_tags = merge(
    var.tags,
    {
      "auto-destroy" = "true"
      "expires-at"   = local.expires_at
      "env"          = var.env_name
    },
  )

  # Naming. Keep these short and predictable so cleanup tooling can match by
  # prefix (`rg-${name_prefix}-`) safely.
  rg_name       = "rg-${var.name_prefix}-${var.env_name}"
  aks_name      = "aks-${var.name_prefix}-${var.env_name}"
  identity_name = "id-${var.name_prefix}-${var.env_name}"

  # ACR names must be 5-50 alphanumeric, no hyphens, and globally unique. The
  # sha1 suffix scopes the name to (subscription, prefix, env) so two users
  # with the same defaults don't collide on a global namespace.
  acr_suffix = substr(sha1("${var.subscription_id}-${var.name_prefix}-${var.env_name}"), 0, 6)
  acr_name   = substr(replace("acr${var.name_prefix}${var.env_name}${local.acr_suffix}", "-", ""), 0, 50)

  # GHA managed identity name. Kept short and predictable.
  gha_identity_name = "id-${var.name_prefix}-${var.env_name}-gha"

  # Default OIDC subjects for the GHA federated credential. Covers
  # main-branch pushes (workflow_dispatch on main, push to main) and
  # pull_request runs from forks-of-this-repo. Engineers can override via
  # var.github_oidc_subjects.
  github_oidc_default_subjects = [
    "repo:${var.github_owner}/${var.github_repo}:ref:refs/heads/main",
    "repo:${var.github_owner}/${var.github_repo}:pull_request",
    "repo:${var.github_owner}/${var.github_repo}:environment:test",
  ]
  github_oidc_subjects = length(var.github_oidc_subjects) > 0 ? var.github_oidc_subjects : local.github_oidc_default_subjects

  # Key Vault names must be 3-24 alphanumeric + hyphens, globally unique.
  # Suffix with a deterministic hash of (subscription, prefix, env) so two
  # different subscriptions don't collide. Right-size the body so the suffix
  # always survives the 24-char cap:
  #   "kv-" (3) + body (<=14) + "-" (1) + suffix (6) = <= 24.
  kv_suffix = substr(sha1("${var.subscription_id}-${var.name_prefix}-${var.env_name}"), 0, 6)
  kv_body   = substr("${var.name_prefix}-${var.env_name}", 0, 14)
  kv_name   = "kv-${local.kv_body}-${local.kv_suffix}"
}
