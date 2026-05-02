# User-assigned managed identity for the daedalus workload, plus the federated
# credential that lets a Kubernetes ServiceAccount in the AKS cluster exchange
# its projected token for an Entra ID token bound to this identity.
#
# The K8s SA must be created separately (by Helm / kustomize) with the
# annotation:
#   azure.workload.identity/client-id: <this module's client_id output>
# and the namespace+name must match var.service_account_namespace +
# var.service_account_name.

resource "azurerm_user_assigned_identity" "this" {
  name                = var.name
  resource_group_name = var.resource_group_name
  location            = var.location
  tags                = var.tags
}

resource "azurerm_federated_identity_credential" "this" {
  name                = "${var.name}-fic"
  resource_group_name = var.resource_group_name
  parent_id           = azurerm_user_assigned_identity.this.id

  audience = ["api://AzureADTokenExchange"]
  issuer   = var.oidc_issuer_url
  subject  = "system:serviceaccount:${var.service_account_namespace}:${var.service_account_name}"
}
