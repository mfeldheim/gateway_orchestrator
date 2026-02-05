variable "namespace" {
  description = "Namespace where Gateway Orchestrator will be deployed"
  type        = string
  default     = "gateway-orchestrator"
}

variable "irsa_role_arn" {
  description = "ARN of the IAM role for IRSA (service account annotation)"
  type        = string
}

variable "image" {
  description = "Container image for Gateway Orchestrator"
  type        = string
  default     = "ghcr.io/mfeldheim/gateway_orchestrator:latest"
}

variable "replicas" {
  description = "Number of controller replicas (for high availability)"
  type        = number
  default     = 2
}

variable "allowed_domains" {
  description = "Comma-separated list of allowed apex domains (e.g., example.com,example.org)"
  type        = string
}

variable "gateway_namespace" {
  description = "Namespace where Gateway resources are managed"
  type        = string
  default     = "edge"
}

variable "gateway_class_name" {
  description = "GatewayClass to use for provisioned Gateways"
  type        = string
  default     = "aws-alb"
}
