# Gateway Orchestrator Terraform Example

This directory contains a complete Terraform configuration for deploying the Gateway Orchestrator controller.

## Prerequisites

- Kubernetes cluster with Gateway API CRDs installed
- AWS Load Balancer Controller deployed
- Terraform >= 1.0
- IAM role for IRSA with required permissions (see [iam.tf](./iam.tf))

## Usage

```bash
# Initialize Terraform
terraform init

# Create terraform.tfvars
cat > terraform.tfvars <<EOF
irsa_role_arn    = "arn:aws:iam::123456789012:role/gateway-orchestrator"
allowed_domains  = "example.com,example.org"
gateway_namespace = "edge"
EOF

# Plan and apply
terraform plan
terraform apply
```

## Required IAM Permissions

The IRSA role must have permissions for:
- ACM (request, describe, delete certificates)
- Route53 (manage DNS records)

See [iam.tf](./iam.tf) for the complete policy.

## Files

- `rbac.tf` - Kubernetes RBAC (ServiceAccount, ClusterRole, ClusterRoleBinding)
- `deployment.tf` - Controller Deployment and Service
- `iam.tf` - AWS IAM role and policy for IRSA
- `variables.tf` - Input variables
- `versions.tf` - Terraform and provider version constraints

## Configuration

Key variables:

| Variable | Description | Default |
|----------|-------------|---------|
| `namespace` | Namespace for controller | `gateway-orchestrator` |
| `irsa_role_arn` | ARN of IAM role for IRSA | (required) |
| `allowed_domains` | Comma-separated allowed domains | (required) |
| `gateway_namespace` | Namespace for Gateway pool | `edge` |
| `gateway_class_name` | GatewayClass to use | `aws-alb` |
| `replicas` | Number of controller replicas | `2` |

## High Availability

The deployment is configured for HA:
- 2 replicas by default
- Leader election enabled
- Pod anti-affinity to spread across nodes
- Readiness/liveness probes

## Monitoring

Metrics are exposed on port 8080 at `/metrics` (Prometheus format).

To scrape with Prometheus Operator:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: gateway-orchestrator
  namespace: gateway-orchestrator
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: gateway-orchestrator
  endpoints:
    - port: metrics
      path: /metrics
      interval: 30s
```
