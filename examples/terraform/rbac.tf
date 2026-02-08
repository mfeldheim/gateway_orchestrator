# Gateway Orchestrator RBAC Configuration
#
# This Terraform module creates the necessary Kubernetes RBAC resources
# for the Gateway Orchestrator controller.

resource "kubernetes_service_account" "gateway_orchestrator" {
  metadata {
    name      = "gateway-orchestrator"
    namespace = var.namespace

    annotations = {
      "eks.amazonaws.com/role-arn" = var.irsa_role_arn
    }
  }
}

resource "kubernetes_cluster_role" "gateway_orchestrator" {
  metadata {
    name = "gateway-orchestrator"
  }

  # Namespace permissions - required for controller caching/watching
  rule {
    api_groups = [""]
    resources  = ["namespaces"]
    verbs      = ["get", "list", "watch"]
  }

  # Gateway Orchestrator CRDs
  rule {
    api_groups = ["gateway.opendi.com"]
    resources = [
      "gatewayhostnamerequests",
      "domainclaims",
      "hostnamegrants"
    ]
    verbs = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  # Gateway Orchestrator CRD status subresources
  rule {
    api_groups = ["gateway.opendi.com"]
    resources = [
      "gatewayhostnamerequests/status",
      "gatewayhostnamerequests/finalizers",
      "hostnamegrants/status"
    ]
    verbs = ["get", "patch", "update"]
  }

  # Gateway API resources
  rule {
    api_groups = ["gateway.networking.k8s.io"]
    resources  = ["gateways", "gatewayclasses"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  # Gateway API status subresources
  rule {
    api_groups = ["gateway.networking.k8s.io"]
    resources  = ["gateways/status"]
    verbs      = ["get", "patch", "update"]
  }

  # Events for observability
  rule {
    api_groups = [""]
    resources  = ["events"]
    verbs      = ["create", "patch"]
  }

  # AWS Load Balancer Controller configuration CRDs
  rule {
    api_groups = ["gateway.k8s.aws"]
    resources  = ["loadbalancerconfigurations"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  # Services for Gateway status inspection
  rule {
    api_groups = [""]
    resources  = ["services"]
    verbs      = ["get", "list", "watch"]
  }

  # Leader election (coordination.k8s.io)
  rule {
    api_groups = ["coordination.k8s.io"]
    resources  = ["leases"]
    verbs      = ["get", "list", "watch", "create", "update", "patch"]
  }
}

resource "kubernetes_cluster_role_binding" "gateway_orchestrator" {
  metadata {
    name = "gateway-orchestrator"
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = kubernetes_cluster_role.gateway_orchestrator.metadata[0].name
  }

  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account.gateway_orchestrator.metadata[0].name
    namespace = kubernetes_service_account.gateway_orchestrator.metadata[0].namespace
  }
}
