# K8s Gateway Orchestrator - Copilot Instructions

## Project Overview

A Kubernetes controller that enables **self-service, project-owned domains** on AWS EKS. The controller reconciles custom resources to provision/maintain AWS + Kubernetes resources for exposing workloads via the **Kubernetes Gateway API** using the **AWS Load Balancer Controller (ALB)**.

**Core capabilities:**
- Request and validate TLS certificates via ACM
- Manage Route53 DNS records (alias to ALB)
- Orchestrate a pool of Gateway resources (each backed by its own ALB)
- Enable project teams to own full domains without manual platform approvals

**Design principles:** No manual approvals, no snowflakes, idempotent reconciliation (IS vs SHOULD), Kubernetes-native best practices.

## Architecture

### Core Components

1. **K8s Gateway Orchestrator Controller** (this project)
   - Watches `GatewayHostnameRequest` and companion CRDs
   - Provisions/updates ACM certificates + validation records
   - Manages Route53 alias records to assigned ALBs
   - Orchestrates Gateway pool scaling (creates new Gateways when capacity reached)
   - Configures Gateway `allowedRoutes` to permit requesting namespace

2. **AWS Load Balancer Controller** (external dependency)
   - Reconciles Gateway/HTTPRoute resources to ALBs and rules

3. **Policy Engine** (recommended: Kyverno or Gatekeeper)
   - Ensures HTTPRoute hostnames match granted hostnames for namespace

### Gateway Pool Strategy

Instead of a single shared ALB, the controller manages an **elastic pool of Gateways** (`gw-01`, `gw-02`, etc.), each backed by its own ALB. Domains are assigned using a first-fit algorithm until a Gateway approaches AWS limits (certificates, rules, target groups), then a new Gateway is created.

### Reconciliation State Machine

Each reconcile is idempotent and follows these steps:

1. **Validate** request (hostname format, required fields, domain allowlist)
2. **Claim** hostname via DomainClaim (first-come-first-serve, atomic)
3. **Request ACM certificate** for the hostname
4. **Create DNS validation records** in Route53
5. **Wait for certificate issuance** (poll ACM status)
6. **Assign to Gateway** from pool (or create new Gateway if needed)
7. **Attach certificate** to ALB listener
8. **Create Route53 alias** record to ALB
9. **Configure allowedRoutes** for namespace (and optionally HostnameGrant)
10. **Mark Ready** when complete

**Cleanup (via finalizer):** Remove Route53 records → Detach cert → Delete ACM cert → Remove validation records → Release DomainClaim

## Build, Test, and Lint

```bash
# Generate CRDs and code
make generate                    # Generate Go code (DeepCopy, etc.)
make manifests                   # Generate CRD YAML manifests

# Build
make build                       # Build controller binary
go build -o bin/controller ./cmd/controller

# Run tests
make test                        # All tests
go test ./...                    # All tests (direct)
go test ./internal/controller/...  # Specific package
go test -v -run TestReconcileGatewayHostnameRequest  # Single test

# Lint
make lint                        # Run linters
golangci-lint run                # Direct linting

# Run locally against cluster
make run                         # Run against configured kubectl context
go run ./cmd/controller          # Direct run

# Build Docker image
make docker-build IMG=controller:latest
docker build -t gateway-orchestrator:latest .
```

## Key Conventions

### Project Structure

```
cmd/controller/              - Main application entry point
api/v1alpha1/                - CRD Go types (GatewayHostnameRequest, DomainClaim, HostnameGrant)
internal/controller/         - Reconcilers (gatewayHostnameRequest, domainClaim)
internal/aws/                - AWS client wrappers (ACM, Route53, ELBv2)
internal/gateway/            - Gateway pool logic (selection, capacity tracking)
internal/policy/             - Hostname grant helpers
config/crd/                  - Generated CRD manifests
config/rbac/                 - Controller RBAC
config/manager/              - Kustomize/Helm deployment manifests
config/samples/              - Sample CRs
```

### Custom Resource Design

**GatewayHostnameRequest** (namespaced):
- `spec.zoneId`: Route53 hosted zone ID (must exist)
- `spec.hostname`: FQDN to expose (e.g., `test.opendi.com`)
- `spec.environment`: env selector (`dev`, `staging`, `prod`)
- `spec.visibility`: `internet-facing` or `internal`
- `spec.gatewayClass`: which GatewayClass to use
- `status.assignedGateway`, `status.certificateArn`, `status.assignedLoadBalancer`
- `status.conditions`: `Claimed`, `CertificateRequested`, `DnsValidated`, `CertificateIssued`, `ListenerAttached`, `DnsAliasReady`, `Ready`

**DomainClaim** (cluster-scoped or infra namespace):
- Atomic lock for `(zoneId, hostname)` to implement first-come-first-serve
- Prevents duplicate domain requests across namespaces

**HostnameGrant** (infra namespace, optional):
- Records which hostnames a namespace is allowed to use
- Consumed by policy engine (Kyverno/Gatekeeper) to enforce Route hostname restrictions

**General CRD conventions:**
- Use `v1alpha1` API version for initial development
- Follow Kubernetes API conventions (spec/status split)
- Status conditions must include `type`, `status`, `reason`, `message`, `lastTransitionTime`
- Use finalizers for cleanup (e.g., `gateway-orchestrator.opendi.com/finalizer`)

### Controller Pattern

- Use **controller-runtime** for Kubernetes controller scaffolding (Kubebuilder)
- Reconcile functions **must be idempotent** and safe to re-run (actual state → desired state)
- Always update **status subresource separately** from spec
- Use **exponential backoff** for retries (via controller-runtime's rate limiter)
- **Never assume state persists** between reconciles; always query actual state
- Prefer **deterministic behavior** over smart optimization (stability > clever rebalancing)
- Store external resource identifiers (ARNs, IDs) in `status` fields for auditability

### AWS Interactions

- Use **AWS SDK for Go v2** for all AWS clients
- All operations **must be idempotent** (safe to retry)
- Keep AWS interactions **behind thin interfaces** for testing/mocking
- Store resource ARNs/IDs in CR status for reconciliation lookups
- Handle **rate limiting and retries** gracefully (use SDK built-in retry logic)
- Tag created resources with `managed-by: gateway-orchestrator` for auditability

**ACM:** Request/describe/delete certificates. Prefer **one cert per hostname** (simpler ownership; SAN bundling is future optimization).

**Route53:** Create/update/delete record sets. Use **ALIAS records** for ALB. ALB canonical hosted zone IDs are well-known per region (see `internal/aws/regions.go`). Clean up validation CNAMEs on deletion.

**No ELBv2 client needed:** AWS Load Balancer Controller manages all ALB operations. We only read Gateway status for LoadBalancer DNS name and extract the region to determine the hosted zone ID for Route53 ALIAS records.

### Certificate Attachment Pattern

**Critical:** We use Gateway API patterns, not direct ALB modification:

1. **Orchestrator updates Gateway annotations** with ACM certificate ARN:
   ```yaml
   annotations:
     alb.ingress.kubernetes.io/certificate-arn: arn:aws:acm:...
   ```

2. **AWS Load Balancer Controller watches Gateway** and attaches certificates to ALB listeners automatically

3. **Orchestrator queries ELBv2** only for LoadBalancer DNS/HostedZoneID to create Route53 ALIAS records

See `docs/certificate-management.md` for detailed explanation.

### Error Handling

- Return `ctrl.Result{Requeue: true}` for **transient errors** (will use backoff)
- Use `ctrl.Result{RequeueAfter: time.Duration}` for **known delays** (e.g., polling ACM certificate issuance every 30s)
- **Don't return errors** for "not found" scenarios during deletion (already deleted = success)
- Log errors with **structured logging** (use controller-runtime's logger with key-value pairs)
- Set appropriate **status conditions** on errors so users understand state
- Cleanup operations should be **best-effort** and safe to retry

### Testing

- **Prefer fast unit tests** over integration tests where possible
- Use **envtest** for integration tests requiring real Kubernetes API
- **Mock AWS clients** using interfaces (keep AWS wrappers thin and mockable)
- Use **table-driven tests** for reconciliation logic and state machine transitions
- Test **edge cases:** claim conflicts, cert issuance failures, missing resources, deletion
- Contract tests for AWS client wrappers to validate expected SDK behavior
- Use `testify/assert` or similar for assertions

## Environment Variables & Configuration

```bash
AWS_REGION              # AWS region for Route53/ACM/ELBv2
ALLOWED_DOMAINS         # Comma-separated list of allowed apex domains (e.g., opendi.com,in-muenchen.de)
GATEWAY_NAMESPACE       # Namespace where Gateway pool lives (default: edge)
GATEWAY_CLASS_NAME      # GatewayClass to use (default: aws-alb)
ENABLE_WEBHOOKS         # Enable admission webhooks (default: false)
METRICS_ADDR            # Metrics server address (default: :8080)
HEALTH_PROBE_ADDR       # Health probe address (default: :8081)
```

**IAM Permissions (IRSA):**
- ACM: `RequestCertificate`, `DescribeCertificate`, `DeleteCertificate`, `AddTagsToCertificate`
- Route53: `ChangeResourceRecordSets`, `GetChange`, `ListResourceRecordSets`
- ELBv2: `DescribeLoadBalancers`, `DescribeListeners`, `ModifyListener`
- Read-only discovery for reconciliation

## Dependencies

**Go Libraries:**
- `sigs.k8s.io/controller-runtime`: Kubernetes controller framework (Kubebuilder)
- `k8s.io/client-go`: Kubernetes API client
- `github.com/aws/aws-sdk-go-v2/service/acm`: AWS Certificate Manager SDK
- `github.com/aws/aws-sdk-go-v2/service/route53`: AWS Route53 SDK
- `sigs.k8s.io/gateway-api`: Gateway API types

**External Components (not in this repo):**
- **AWS Load Balancer Controller**: Reconciles Gateway → ALB (manages all ALB operations)
- **Gateway API CRDs**: `GatewayClass`, `Gateway`, `HTTPRoute`
- **Policy Engine** (recommended): Kyverno or Gatekeeper for hostname enforcement

## Development Workflow

1. Define or update CRD types in `api/v1alpha1/`
2. Run `make generate` to update DeepCopy and other generated code
3. Run `make manifests` to regenerate CRD YAML in `config/crd/`
4. Implement reconciliation logic in `internal/controller/`
5. Add unit tests alongside implementation
6. Update RBAC in `config/rbac/` if new Kubernetes permissions needed
7. Test locally: `make run` against a dev EKS cluster (or kind for basic testing)
8. Validate changes don't break existing reconciliation (idempotence check)

**When adding AWS functionality:**
- Create interface in `internal/aws/`
- Implement real client and mock for testing
- Add contract tests to validate SDK usage

## Gateway Pool Scaling

**Capacity tracking:** Monitor per-Gateway metrics:
- Certificate count (SNI limit on ALB listener)
- Rule count (listener rules limit)
- Target group count (ALB target group limit)

**Placement algorithm:** Use **first-fit** (check Gateways in order, pick first with capacity). Avoid rebalancing existing assignments for stability.

**Creating new Gateways:** When no Gateway has capacity, create `gw-{N+1}` in the gateway namespace with appropriate annotations for AWS Load Balancer Controller.

## Safety & Multi-Tenancy

**Minimum safety measures:**
- **Atomic DomainClaim:** Deny duplicate `(zoneId, hostname)` requests
- **Domain allowlist:** Restrict hostnames to known apex domains (prevent accidental use of unrelated zones)
- **RBAC:** Limit who can create GatewayHostnameRequest resources

**Policy enforcement (strongly recommended):**
- Controller creates `HostnameGrant` for each approved hostname+namespace
- Kyverno/Gatekeeper policy ensures `HTTPRoute.spec.hostnames` only contains granted hostnames
- Prevents team A from creating Routes for team B's domains

## Observability

**Emit:**
- **Kubernetes Events** on key transitions (cert requested, ready, failed)
- **Prometheus metrics:** reconcile duration, error counts, retry counts, gateway count, hostname count
- **Structured logs** with `namespace`, `name`, `hostname`, `zoneId`, `gatewayName`

**Recommended alerts:**
- High reconcile error rate (> 10% over 5min)
- ACM issuance stuck (pending validation > 30min)
- Route53 change failures
- ALB listener attach failures

## Code Style

- **Simplicity over cleverness:** Choose the simpler, safer, more observable solution
- **Avoid custom DSLs:** Keep CRDs small and composable
- **No secrets in Kubernetes:** Use ACM for certs, IRSA for credentials
- **Document failure modes:** Add comments explaining what happens when AWS API fails, resources missing, etc.
- **Minimal comments:** Only comment non-obvious logic or failure handling
