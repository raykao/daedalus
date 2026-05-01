resource "kubernetes_namespace" "keda" {
  metadata {
    name = "keda"
  }
  depends_on = [azurerm_kubernetes_cluster.main]
}

resource "helm_release" "keda" {
  name             = "keda"
  repository       = "https://kedacore.github.io/charts"
  chart            = "keda"
  version          = var.keda_chart_version
  namespace        = kubernetes_namespace.keda.metadata[0].name
  create_namespace = false
  wait             = true
  timeout          = 300

  depends_on = [kubernetes_namespace.keda]
}
