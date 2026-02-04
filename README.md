# Gateway Orchestrator

Self-service domain management for Kubernetes. Teams request domains, the orchestrator handles the rest.

## What is it?

Gateway Orchestrator is a Kubernetes controller that automates domain exposure on AWS EKS. When a team creates a `GatewayHostnameRequest`, the controller:

1. Provisions an ACM certificate
2. Validates it via Route53 DNS
3. Assigns the hostname to an ALB-backed Gateway
4. Creates DNS alias records pointing to the load balancer
5. Grants the namespace permission to create routes for that hostname

All of this happens automatically—no tickets, no manual approvals, no waiting.

## What problem does it solve?

In multi-tenant Kubernetes clusters, exposing services externally requires managing infrastructure-level resources (Gateways, load balancers, certificates) that application deployers shouldn't have permissions to modify:

- **Separation of concerns** — Application deployment tools (ArgoCD, Flux) shouldn't need write access to Gateway resources, which are infrastructure-level concerns
- **Self-service without over-privileging** — Teams need to expose services without being granted broad infrastructure permissions
- **Manual certificate management** — Without automation, someone has to request, validate, and attach certs for every hostname
- **Shared load balancer bottlenecks** — One ALB for everyone hits AWS limits fast (certificates, rules, target groups)
- **Security gaps** — Nothing prevents Team A from claiming Team B's hostname in their routes

Gateway Orchestrator solves this by letting teams **request** what they need via a simple CRD, while the controller handles all infrastructure provisioning automatically. Application deployers only manage HTTPRoutes—no Gateway permissions required.

## Prerequisites

- Kubernetes 1.28+ with Gateway API CRDs installed
- [AWS Load Balancer Controller](https://kubernetes-sigs.github.io/aws-load-balancer-controller/) v2.6+
- Route53 hosted zone(s) for your domains
- IRSA-enabled service account with permissions for ACM, Route53, and ELBv2

## Installation

### Using Kustomize

```bash
# Install CRDs
kubectl apply -k https://github.com/opendi/gateway_orchestrator/config/crd

# Install controller
kubectl apply -k https://github.com/opendi/gateway_orchestrator/config/default
```

### Using kubectl

```bash
# Clone the repo
git clone https://github.com/opendi/gateway_orchestrator.git
cd gateway_orchestrator

# Install CRDs
kubectl apply -f config/crd/bases/

# Install controller (adjust image and IRSA role as needed)
kubectl apply -k config/default/
```

### Required AWS IAM Permissions

The controller needs these AWS permissions (attach via IRSA):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "acm:RequestCertificate",
        "acm:DescribeCertificate",
        "acm:DeleteCertificate",
        "acm:ListCertificates"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "route53:ChangeResourceRecordSets",
        "route53:ListResourceRecordSets"
      ],
      "Resource": "arn:aws:route53:::hostedzone/*"
    }
  ]
}
```

## Usage

### Request a hostname

Create a `GatewayHostnameRequest` in your namespace:

```yaml
apiVersion: gateway.opendi.com/v1alpha1
kind: GatewayHostnameRequest
metadata:
  name: my-api
  namespace: my-team
spec:
  hostname: api.example.com
  zoneId: Z1234567890ABC  # Your Route53 hosted zone ID
  environment: prod       # Optional: dev, staging, prod
  visibility: internet-facing  # Optional: internet-facing or internal
```

Apply it:

```bash
kubectl apply -f gatewayhostnamerequest.yaml
```

### Check status

```bash
kubectl get gatewayhostnamerequests -n my-team

NAME     HOSTNAME          GATEWAY   READY   AGE
my-api   api.example.com   gw-01     True    5m
```

View detailed status:

```bash
kubectl describe ghr my-api -n my-team
```

The controller progresses through these conditions:
- `Claimed` — hostname reserved (first-come-first-serve)
- `CertificateRequested` — ACM certificate created
- `DnsValidated` — validation records created in Route53
- `CertificateIssued` — ACM certificate is active
- `ListenerAttached` — certificate attached to Gateway/ALB
- `DnsAliasReady` — A/AAAA records point to the ALB
- `Ready` — everything is provisioned

### Create routes to your service

Once `Ready=True`, create an `HTTPRoute` in your namespace:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: my-api-route
  namespace: my-team
spec:
  parentRefs:
    - name: gw-01           # From GatewayHostnameRequest status
      namespace: edge
  hostnames:
    - api.example.com
  rules:
    - backendRefs:
        - name: my-api-service
          port: 8080
```

Traffic now flows: `api.example.com` → ALB → your service.

## CRD Reference

### GatewayHostnameRequest

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `spec.hostname` | string | Yes | FQDN to expose (e.g., `api.example.com`) |
| `spec.zoneId` | string | Yes | Route53 hosted zone ID |
| `spec.environment` | string | No | `dev`, `staging`, or `prod` |
| `spec.visibility` | string | No | `internet-facing` (default) or `internal` |
| `spec.gatewayClass` | string | No | GatewayClass name (default: `aws-alb`) |
| `spec.gatewaySelector` | LabelSelector | No | Restrict which Gateways can be used |

### Supporting CRDs

- **DomainClaim** (cluster-scoped): Implements first-come-first-serve hostname reservation. Created automatically by the controller.
- **HostnameGrant** (edge namespace): Records which namespaces can use which hostnames. Used by policy engines (Kyverno/Gatekeeper) to enforce route ownership.

## How it works

```
┌─────────────────────────────────────────────────────────────────┐
│  Team Namespace                                                  │
│  ┌──────────────────────┐    ┌──────────────────────┐           │
│  │ GatewayHostnameRequest│    │ HTTPRoute             │          │
│  │ hostname: api.example │───▶│ hostnames: [api.ex...] │         │
│  └──────────────────────┘    └──────────────────────┘           │
└─────────────────────────────────────────────────────────────────┘
                │
                │ reconciles
                ▼
┌─────────────────────────────────────────────────────────────────┐
│  Gateway Orchestrator                                            │
│  • Creates ACM certificate                                       │
│  • Adds DNS validation records                                   │
│  • Assigns to Gateway with capacity                              │
│  • Attaches cert to ALB listener                                 │
│  • Creates Route53 alias to ALB                                  │
│  • Creates HostnameGrant for policy enforcement                  │
└─────────────────────────────────────────────────────────────────┘
                │
                ▼
┌─────────────────────────────────────────────────────────────────┐
│  Edge Namespace                                                  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                       │
│  │ Gateway  │  │ Gateway  │  │ Gateway  │  (auto-scaled pool)   │
│  │ gw-01    │  │ gw-02    │  │ gw-03    │                       │
│  └──────────┘  └──────────┘  └──────────┘                       │
│       │                                                          │
│       ▼                                                          │
│  ┌──────────────────────────────────────┐                       │
│  │ AWS Load Balancer Controller          │                       │
│  │ → creates/manages ALBs                │                       │
│  └──────────────────────────────────────┘                       │
└─────────────────────────────────────────────────────────────────┘
```

## Security recommendations

1. **Restrict who can create requests** — Use RBAC to limit `GatewayHostnameRequest` creation
2. **Enforce hostname ownership** — Deploy Kyverno or Gatekeeper policies that validate `HTTPRoute.spec.hostnames` against `HostnameGrant` objects
3. **Allowlist domains** — Configure the controller to only accept hostnames under your approved apex domains

## Troubleshooting

**Request stuck on `CertificateRequested`**
- Check if DNS validation records were created in Route53
- Verify the zoneId is correct and the controller has Route53 permissions

**Request stuck on `CertificateIssued`**
- The Gateway pool may be full; check if a new Gateway is being created
- Verify AWS Load Balancer Controller is running and healthy

**HTTPRoute not working**
- Confirm `GatewayHostnameRequest` shows `Ready=True`
- Check that `parentRefs` in your HTTPRoute matches the assigned Gateway
- Verify your namespace is allowed in the Gateway's `allowedRoutes`

## License

Apache 2.0
