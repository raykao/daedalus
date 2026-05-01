# Terraform targets for the Daedalus test AKS cluster.
#
# Usage:
#   make init       - initialize providers and modules
#   make plan       - show planned changes (requires cluster from Phase 1 on a fresh workspace)
#   make deploy     - apply in two phases: AKS first, then KEDA
#   make destroy    - tear down all resources
#
# Why two phases?
# The kubernetes and helm providers are configured from the AKS cluster
# outputs. On a fresh workspace, the cluster does not yet exist, so
# provider credentials are unknown at plan time. Phase 1 applies only
# the cloud infrastructure (AKS, ACR, resource group). Phase 2 applies
# the cluster-level operators (KEDA) once credentials are available.
#
# Prerequisites: az login, terraform >= 1.7, helm >= 3.x

.PHONY: init plan deploy destroy fmt validate

init:
	terraform init

plan: init
	@echo "Note: 'terraform plan' may fail on a fresh workspace if the AKS cluster does not exist yet."
	@echo "      Run 'make deploy' first to provision the cluster, then use 'make plan' for subsequent changes."
	terraform plan

deploy: init
	@echo "Phase 1: provisioning AKS cluster, ACR, and resource group..."
	terraform apply \
		-target=azurerm_resource_group.main \
		-target=azurerm_kubernetes_cluster.main \
		-target=azurerm_container_registry.main \
		-target=azurerm_role_assignment.aks_acr_pull
	@echo "Phase 2: installing cluster operators (KEDA)..."
	terraform apply

destroy: init
	terraform destroy

fmt:
	terraform fmt -recursive

validate: init
	terraform validate
