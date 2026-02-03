# Gateway Orchestrator - Developer Setup

## Prerequisites

- Go 1.22 or later
- kubectl configured with access to a Kubernetes cluster (EKS for full functionality)
- AWS credentials configured (for ACM, Route53, ELBv2 access)
- AWS Load Balancer Controller installed in the cluster
- Gateway API CRDs installed

## Quick Start

1. **Clone and build:**
   ```bash
   git clone https://github.com/michelfeldheim/gateway-orchestrator.git
   cd gateway-orchestrator
   make build
   ```

2. **Install CRDs:**
   ```bash
   make install
   ```

3. **Run locally (against configured kubectl context):**
   ```bash
   export AWS_REGION=us-east-1
   make run
   ```

4. **Create a sample GatewayHostnameRequest:**
   ```bash
   kubectl apply -f config/samples/gateway_v1alpha1_gatewayhostnamerequest.yaml
   ```

5. **Check status:**
   ```bash
   kubectl get ghr -n my-app
   kubectl describe ghr example-hostname -n my-app
   ```

## Development Workflow

See the comprehensive workflow in [.github/copilot-instructions.md](.github/copilot-instructions.md).

### Running Tests

```bash
make test
```

### Generating Code

After modifying CRD types in `api/v1alpha1/`:

```bash
make generate  # Generate DeepCopy methods
make manifests # Generate CRD YAML
```

### Linting

```bash
make lint
```

## Project Structure

```
.
├── api/v1alpha1/              # CRD type definitions
├── cmd/controller/            # Main entry point
├── config/
│   ├── crd/                   # Generated CRD manifests
│   ├── samples/               # Example CR manifests
│   ├── rbac/                  # RBAC manifests
│   └── manager/               # Deployment manifests
├── internal/
│   ├── controller/            # Reconciliation logic
│   ├── aws/                   # AWS client wrappers
│   ├── gateway/               # Gateway pool management
│   └── policy/                # Policy helpers
└── Makefile
```

## Current Status

✅ **Completed:**
- CRD definitions (GatewayHostnameRequest, DomainClaim, HostnameGrant)
- AWS client interfaces (ACM, Route53, ELBv2)
- Gateway pool logic (selection, capacity tracking)
- Core reconciliation framework
- Domain claim mechanism (first-come-first-serve)
- Certificate request and validation flow
- Finalizer and cleanup logic

🚧 **In Progress:**
- Gateway assignment and listener attachment
- Route53 ALIAS record creation
- AllowedRoutes configuration
- Full AWS SDK implementation (currently mocked)
- Comprehensive testing suite

📋 **Planned:**
- HostnameGrant policy integration
- Prometheus metrics
- Kubernetes event emission
- Full RBAC and deployment manifests
- Operational documentation

## Contributing

This is an active development project. See [README.md](README.md) for architecture details and design goals.
