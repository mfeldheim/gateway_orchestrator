resource "kubernetes_deployment" "gateway_orchestrator" {
  metadata {
    name      = "gateway-orchestrator"
    namespace = var.namespace

    labels = {
      "app.kubernetes.io/name"    = "gateway-orchestrator"
      "app.kubernetes.io/version" = "v0.1.0"
    }
  }

  spec {
    replicas = var.replicas

    selector {
      match_labels = {
        "app.kubernetes.io/name" = "gateway-orchestrator"
      }
    }

    template {
      metadata {
        labels = {
          "app.kubernetes.io/name" = "gateway-orchestrator"
        }
      }

      spec {
        service_account_name = kubernetes_service_account.gateway_orchestrator.metadata[0].name

        # Anti-affinity for high availability
        affinity {
          pod_anti_affinity {
            preferred_during_scheduling_ignored_during_execution {
              weight = 100
              pod_affinity_term {
                label_selector {
                  match_labels = {
                    "app.kubernetes.io/name" = "gateway-orchestrator"
                  }
                }
                topology_key = "kubernetes.io/hostname"
              }
            }
          }
        }

        container {
          name  = "controller"
          image = var.image

          args = [
            "--leader-elect",
            "--health-probe-bind-address=:8081",
            "--metrics-bind-address=:8080"
          ]

          env {
            name  = "ALLOWED_DOMAINS"
            value = var.allowed_domains
          }

          env {
            name  = "GATEWAY_NAMESPACE"
            value = var.gateway_namespace
          }

          env {
            name  = "GATEWAY_CLASS_NAME"
            value = var.gateway_class_name
          }

          # Health and readiness probes
          liveness_probe {
            http_get {
              path = "/healthz"
              port = 8081
            }
            initial_delay_seconds = 15
            period_seconds        = 20
          }

          readiness_probe {
            http_get {
              path = "/readyz"
              port = 8081
            }
            initial_delay_seconds = 5
            period_seconds        = 10
          }

          resources {
            requests = {
              cpu    = "100m"
              memory = "128Mi"
            }
            limits = {
              cpu    = "500m"
              memory = "512Mi"
            }
          }

          security_context {
            allow_privilege_escalation = false
            read_only_root_filesystem  = true
            run_as_non_root            = true
            run_as_user                = 65532

            capabilities {
              drop = ["ALL"]
            }
          }
        }

        security_context {
          run_as_non_root = true
          seccomp_profile {
            type = "RuntimeDefault"
          }
        }
      }
    }
  }
}

resource "kubernetes_service" "gateway_orchestrator_metrics" {
  metadata {
    name      = "gateway-orchestrator-metrics"
    namespace = var.namespace

    labels = {
      "app.kubernetes.io/name" = "gateway-orchestrator"
    }
  }

  spec {
    selector = {
      "app.kubernetes.io/name" = "gateway-orchestrator"
    }

    port {
      name        = "metrics"
      port        = 8080
      target_port = 8080
      protocol    = "TCP"
    }

    type = "ClusterIP"
  }
}
