

# K8s Gateway Orchestrator

A Kubernetes controller (written in Go) that enables **self-service, project-owned domains** on AWS while keeping the platform safe and operationally boring.

The controller reconciles a custom resource (e.g. `GatewayHostnameRequest`) and provisions/maintains the AWS + Kubernetes resources required to expose workloads via the **Kubernetes Gateway API** on **Amazon EKS** using the **AWS Load Balancer Controller (ALB)**.

> Design goals: **no manual approvals**, **no snowflakes**, **idempotent reconciliation (IS vs SHOULD)**, and **Kubernetes-native best practices**.

---

## Problem Statement

We run a multi-tenant EKS cluster with many projects. Projects must be able to:

- Own **full domains** (apex and subdomains) in Route53 (some projects have multiple domains).
- Request and validate TLS certificates via ACM.
- Attach those certificates to an ALB-backed Gateway.
- Create `HTTPRoute` resources in their namespace to route traffic to their Services.

The platform must:

- Avoid a single shared ALB becoming a scaling/limit bottleneck.
- Enforce safety boundaries (prevent hostile/accidental domain hijacking).
- Recover from controller restarts/state loss by reconciling **actual state** to **desired state**.
- Keep “global” infrastructure changes controlled (manual apply), while project-level configuration is GitOps/CI-driven.

---

## High-Level Approach

### Kubernetes is the source of truth

- **Desired state** is expressed via CRDs (`GatewayHostnameRequest`, and optionally `DomainClaim`/`HostnameGrant`).
- The controller records the state of external resources via `status` fields + conditions.
- Reconciliation is **idempotent** and can always be re-run.

### Edge is an elastic pool

Instead of forcing everything into 1–2 shared ALBs, the controller manages a **pool of Gateways**, each backed by its own ALB. Domains are assigned to a Gateway until it approaches AWS limits (cert count, rules, target groups, etc.), then a new Gateway is created.

### No wheel reinvention

We intentionally reuse:

- **Gateway API** for routing primitives (`GatewayClass`, `Gateway`, `HTTPRoute`).
- **AWS Load Balancer Controller** to create/manage ALBs.
- **ACM** for certificate lifecycle.
- **Route53** for DNS and ACM DNS validation.
- **Policy-as-code** (Kyverno or Gatekeeper) to enforce hostname ownership on Routes.

---

## Non-Goals

- Replacing AWS Load Balancer Controller.
- Acting as a full DNS registrar / domain acquisition system.
- Deep L7 traffic policy (rate limiting, auth, etc.). Use WAF / service mesh / API gateway if required.
- Perfect “smart rebalancing” of domains between ALBs. We prefer **stability** and minimal churn.

---

## Architecture

### Components

1. **K8s Gateway Orchestrator Controller** (this project)
   - Watches `GatewayHostnameRequest` (and optional companion CRDs).
   - Provisions/updates:
     - ACM certificates + validation records
     - Route53 alias records to the assigned ALB
     - Gateway pool scaling (create additional Gateways when needed)
     - Gateway `allowedRoutes` rules to allow the requesting namespace

2. **AWS Load Balancer Controller**
   - Reconciles Gateways/Routes to ALBs and rules.

3. **Policy Engine (recommended)**
   - Ensures `HTTPRoute.spec.hostnames` may only include hostnames granted to that namespace.

### Control Plane Objects

- `GatewayClass` (cluster-scoped): references AWS Load Balancer Controller.
- `Gateway` (edge namespace): created/managed as a pool `gw-01`, `gw-02`, ...
- `HTTPRoute` (project namespace): owned by project teams.

### Custom Resources

The exact CRD names can be adjusted, but the recommended set is:

- **`GatewayHostnameRequest`** (namespaced): request to expose one hostname/domain.
- **`DomainClaim`** (optional, cluster-scoped or infra-namespace): atomic lock to implement first-come-first-serve.
- **`HostnameGrant`** (optional, infra-namespace): record of hostnames a namespace is allowed to use (policy engine consumes this).

---

## CRD: GatewayHostnameRequest (concept)

> This is the contract between platform and project teams.

Typical fields:

- `spec.zoneId`: Route53 hosted zone id (must exist)
- `spec.hostname`: FQDN to expose (e.g. `test.opendi.com`)
- `spec.environment`: logical env selector (e.g. `dev`, `staging`, `prod`)
- `spec.visibility`: `internet-facing` vs `internal`
- `spec.gatewayClass`: which GatewayClass to use
- `spec.routePolicy`: whether to auto-allow the namespace (via `allowedRoutes`) and any constraints
- `spec.dns`: desired records (`A/AAAA ALIAS`, optionally `www` redirects, etc.)

Status fields (controller-managed):

- `status.assignedGateway`: name/namespace
- `status.assignedLoadBalancer`: ALB DNS name / ARN if available
- `status.certificateArn`: ACM certificate ARN
- `status.conditions`: `Claimed`, `CertificateRequested`, `DnsValidated`, `CertificateIssued`, `ListenerAttached`, `DnsAliasReady`, `Ready`

---

## Reconciliation State Machine

The controller must be stateless and recoverable. Each reconcile should:

1. **Validate request**
   - Required fields present
   - Hostname format + allowed domain policy (optional allowlist)

2. **Claim (first-come-first-serve)**
   - Create/ensure a `DomainClaim` for `(zoneId, hostname)`
   - If claim exists and not owned by this request -> set condition `Denied` and stop.

3. **Ensure ACM certificate**
   - Create or reuse a cert for the exact hostname.
   - Prefer one cert per request (simple ownership). SAN bundling can be a future optimization.

4. **Ensure DNS validation records**
   - Write required CNAME records for ACM DNS validation into the provided `zoneId`.

5. **Wait for issuance**
   - Poll/observe ACM until `ISSUED`.

6. **Assign to a Gateway**
   - Choose a Gateway from the pool that has capacity.
   - If none fits, create a new Gateway in the pool.

7. **Attach certificate to listener**
   - Ensure the ALB listener serving this Gateway has the cert attached.
   - Use controller-supported patterns (AWS LBC currently works with ACM ARNs).

8. **Ensure Route53 alias**
   - Create/ensure `A/AAAA ALIAS` to the assigned ALB.

9. **Allow Routes for the namespace**
   - Update Gateway `allowedRoutes` to permit the request’s namespace (or label selector).
   - Optionally create/update `HostnameGrant` for hostname-level policy enforcement.

10. **Finalize**
   - Mark `Ready=True`.

### Deletion / Cleanup

With a finalizer on `GatewayHostnameRequest`:

- Remove Route53 alias records
- Detach cert from listener (optional)
- Delete ACM certificate (if owned exclusively)
- Delete validation records (optional)
- Release `DomainClaim`

> Cleanup must be best-effort and safe to retry.

---

## Multi-Tenancy & Safety

### Minimum safety (no tags, no manual approvals)

Even with “assume ownership on request”, implement guardrails:

- **Atomic claims**: deny duplicate `(zoneId, hostname)` requests.
- **Domain allowlist (recommended)**: limit hostnames to a known list of apex domains we operate (e.g. `opendi.com`, `in-muenchen.de`, ...). This prevents accidental use of unrelated domains.
- **Route hostname enforcement (strongly recommended)**:
  - Use Kyverno/Gatekeeper to ensure `HTTPRoute.spec.hostnames` are allowed for the namespace.
  - Controller creates `HostnameGrant` objects; policy checks every Route.

### Why policy-as-code matters

Without a policy engine, a team could accidentally create a Route claiming another team’s hostname (even if they don’t own the DNS). Policy ensures hostnames are only used by the approved namespace.

---

## AWS Limits & Gateway Pool Scaling

ALBs have practical limits that become relevant in shared environments:

- Number of certificates per listener (SNI)
- Rules per listener
- Target groups per ALB

The orchestrator should keep Gateways small enough to avoid limit pressure:

- Maintain an internal model of “load” per Gateway (certs/rules/targets).
- Place new hostnames using a stable algorithm (first-fit or consistent hash).
- Avoid rebalancing existing domains unless explicitly requested.

---

## Repository Layout (recommended)

```
.
├── cmd/
│   └── controller/                 # main entrypoint
├── api/
│   └── v1alpha1/                   # CRD Go types
├── internal/
│   ├── controller/                 # reconcilers
│   ├── aws/                        # thin AWS clients (ACM, Route53, ELBv2)
│   ├── gateway/                    # gateway pool logic
│   └── policy/                     # hostname grant helpers
├── config/
│   ├── crd/                        # generated CRDs
│   ├── rbac/                       # controller RBAC
│   ├── manager/                    # kustomize/helm manifests
│   └── samples/                    # sample CRs
├── charts/                         # optional helm chart
└── Makefile
```

---

## Technology Choices

- **Go** with `controller-runtime` / Kubebuilder scaffolding.
- AWS SDK for Go v2 for AWS APIs.
- Workqueue-based reconciliation with backoff.
- Structured logging, metrics, and events.

---

## Development

### Prerequisites

- Go (latest stable)
- kubebuilder tools (controller-gen)
- access to a dev EKS cluster (or kind for unit/integration where possible)

### Common tasks

- Generate CRDs:
  - `make generate`
  - `make manifests`

- Run locally against a cluster:
  - `make run`

- Unit tests:
  - `make test`

- Lint:
  - `make lint`

> Prefer fast unit tests and contract tests for AWS client wrappers; use integration tests sparingly.

---

## Deployment

### Global (platform-owned, manually applied)

Platform repo should install:

- AWS Load Balancer Controller
- GatewayClass
- Base `Gateway` pool namespace (`edge`)
- Policy engine (Kyverno or Gatekeeper) and baseline policies
- This controller (Orchestrator)

### Project-owned (GitOps/CI)

Project repos should apply:

- `GatewayHostnameRequest`
- `HTTPRoute` + app manifests

---

## IAM & Permissions

The controller uses IRSA and must have AWS permissions for:

- ACM: request/describe/delete certificates
- Route53: change record sets in specified hosted zones
- ELBv2: describe/modify listeners (attach certificates)
- Read-only discovery permissions for reconciliation

**Principles:**

- Least privilege where possible.
- Explicitly document required permissions.
- Prefer deterministic resource names and storing ARNs in `status` for auditability.

---

## Observability

Controller should emit:

- Kubernetes Events on important transitions
- Prometheus metrics (reconcile duration, error counts, retries)
- Logs with request identifiers (`namespace/name`, `hostname`, `zoneId`)

Recommended dashboards/alerts:

- High reconcile error rate
- ACM issuance stuck (pending validation)
- Route53 change failures
- ALB listener attach failures

---

## Sample Workflow

1. Project creates hosted zone in Route53 (already exists).
2. Project applies a `GatewayHostnameRequest`:
   - `hostname: test.opendi.com`
   - `zoneId: A1231313`
3. Orchestrator provisions cert + validation + ALB attachment + alias.
4. Project creates `HTTPRoute` in its namespace pointing to its Service.
5. Traffic flows.

---

## Security Notes

Even if we currently “assume ownership on request”, treat this controller as a high-privilege component.

Minimum required protections:

- Restrict who can create `GatewayHostnameRequest` (RBAC).
- Consider an allowed apex-domain list.
- Enforce hostname usage on `HTTPRoute` using policy-as-code.

---

## Roadmap (suggested)

- v1alpha1: single-hostname requests, 1 cert per request, simple gateway pool scaling.
- v1beta1: hostname grants + policy integration, better placement metrics.
- v1: optional SAN bundling, improved cleanup policies, drift detection hardening.

---

## Contributing / Agent Guidance

Code agents working on this repo should:

- Prefer **Kubernetes-native patterns** (controller-runtime, status conditions, finalizers).
- Keep reconciliation **idempotent** and **safe to retry**.
- Avoid custom DSLs; keep CRDs small and composable.
- Avoid storing secrets in Kubernetes; use ACM.
- Keep AWS interactions behind thin interfaces for testing.
- Document behavior and failure modes.

If in doubt, choose the solution that is:

1) simpler,
2) safer,
3) more observable,
4) easiest to recover after state loss.
