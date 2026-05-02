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

  # ACR names must be 5-50 alphanumeric, no hyphens. Build deterministically
  # from prefix + env so re-applies are stable.
  acr_name = substr(replace("acr${var.name_prefix}${var.env_name}", "-", ""), 0, 50)

  # Key Vault names must be 3-24 alphanumeric + hyphens, globally unique.
  # Suffix with a deterministic hash of (subscription, prefix, env) so two
  # different subscriptions don't collide.
  kv_suffix = substr(sha1("${var.subscription_id}-${var.name_prefix}-${var.env_name}"), 0, 6)
  kv_name   = substr("kv-${var.name_prefix}-${var.env_name}-${local.kv_suffix}", 0, 24)
}
