# Certificate Management with AWS Load Balancer Controller

## Overview

This document explains how the Gateway Orchestrator integrates with AWS Load Balancer Controller for certificate management using the `LoadBalancerConfiguration` CRD.

## Key Architecture Principle

**We manage four things:**
1. **Gateway resources** (Kubernetes API)
2. **LoadBalancerConfiguration resources** (AWS LBC CRD for certificate/scheme config)
3. **Route53 DNS records** (AWS Route53 API)
4. **ACM certificates** (AWS ACM API)

**AWS Load Balancer Controller manages everything else** (ALB provisioning, listener configuration, certificate attachment to ALB).

## How It Works

### 1. Gateway Creation

When creating a Gateway, we reference a `LoadBalancerConfiguration`:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: gw-01
  namespace: edge
  annotations:
    gateway.opendi.com/visibility: internet-facing
    gateway.opendi.com/certificate-count: "0"
spec:
  gatewayClassName: aws-alb
  infrastructure:
    parametersRef:
      group: gateway.k8s.aws
      kind: LoadBalancerConfiguration
      name: gw-01-config
  listeners:
    - name: https
      protocol: HTTPS
      port: 443
      tls:
        mode: Terminate
        options:
          gateway.opendi.com/acm-managed: "true"
    - name: http
      protocol: HTTP
      port: 80
```

Note: Gateway API validation requires `certificateRefs` or `options` for TLS listeners. We use `options` with a marker annotation since actual certificates are managed via `LoadBalancerConfiguration`.

### 2. LoadBalancerConfiguration

For each Gateway, we create a corresponding `LoadBalancerConfiguration`:

```yaml
apiVersion: gateway.k8s.aws/v1beta1
kind: LoadBalancerConfiguration
metadata:
  name: gw-01-config
  namespace: edge
spec:
  scheme: internet-facing
  listenerConfigurations:
    - protocolPort: HTTPS:443
      defaultCertificate: arn:aws:acm:eu-west-1:123456789:certificate/abc123
      certificates:
        - arn:aws:acm:eu-west-1:123456789:certificate/def456
        - arn:aws:acm:eu-west-1:123456789:certificate/ghi789
    - protocolPort: HTTP:80
```

**AWS Load Balancer Controller does:**
- Reads `LoadBalancerConfiguration` via Gateway's `infrastructure.parametersRef`
- Provisions ALB with the specified scheme
- Configures HTTPS listener with the certificates (first is default, rest are SNI)
- Updates Gateway status with LoadBalancer address

### 3. Certificate Attachment Flow

When a `GatewayHostnameRequest` is reconciled and an ACM certificate is issued:

**We do:**
```go
// 1. Collect all certificate ARNs for the Gateway
arns, _ := r.getGatewayCertificateARNs(ctx, gatewayName, gatewayNamespace)

// 2. Create or update LoadBalancerConfiguration
r.ensureLoadBalancerConfiguration(ctx, gatewayName, gatewayNamespace, arns, visibility)
```

This ensures all certificates from all `GatewayHostnameRequests` assigned to a Gateway are included in its `LoadBalancerConfiguration`.

### 4. Route53 ALIAS Record Creation

**We do:**
```go
// 1. Get LoadBalancer DNS from Gateway status (populated by AWS LBC)
lbDNS := gw.Status.Addresses[0].Value
// Example: k8s-edge-gw01-abc123.eu-west-1.elb.amazonaws.com

// 2. Extract region from DNS name
region := aws.ExtractRegionFromALBDNS(lbDNS)  // "eu-west-1"

// 3. Look up well-known ALB hosted zone ID for the region
hostedZoneID := aws.GetALBHostedZoneID(region)  // "Z32O12XQLNTSW2"

// 4. Create Route53 ALIAS record
record := aws.DNSRecord{
    Name: "test.example.com",
    Type: "A",
    AliasTarget: &aws.AliasTarget{
        DNSName:      lbDNS,
        HostedZoneID: hostedZoneID,  // ALB's canonical zone
    },
}
route53Client.CreateOrUpdateRecord(ctx, userZoneId, record)
```

## Implementation Flow

```
GatewayHostnameRequest Created
         ↓
[1] Request ACM Certificate
         ↓
[2] Create DNS Validation Records (Route53)
         ↓
[3] Wait for Certificate Issuance
         ↓
[4] Select/Create Gateway
         ↓
[5] Sync LoadBalancerConfiguration ← WE DO THIS
    (collect all certs for Gateway, update config)
         ↓
    AWS Load Balancer Controller ← AWS LBC DOES THIS
         ↓
    Provisions ALB + Attaches Certificates
         ↓
    Updates Gateway Status
         ↓
[6] Read Gateway Status ← WE DO THIS
    (get LoadBalancer DNS)
         ↓
[7] Extract Region from DNS
         ↓
[8] Lookup Hosted Zone ID (constants)
         ↓
[9] Create Route53 ALIAS Record
         ↓
    READY ✓
```

## What We Manage vs AWS Load Balancer Controller

### We Manage
✅ Gateway resources (create, update)  
✅ LoadBalancerConfiguration resources (create, update with certs)  
✅ ACM certificates (request, describe, delete)  
✅ Route53 DNS records (validation CNAMEs, ALIAS records)  
✅ DomainClaims (first-come-first-serve)  
✅ Status tracking and conditions

### AWS Load Balancer Controller Manages
✅ ALB provisioning (create, update, delete)  
✅ ALB listeners (HTTP, HTTPS)  
✅ Certificate attachment to ALB listeners  
✅ SNI configuration  
✅ Target groups  
✅ Security groups  
✅ Gateway status updates (LoadBalancer address)

### We Do NOT Manage
❌ ELBv2 API calls (no direct ALB operations)  
❌ Listener management  
❌ Target group management  
❌ Security group management

## Benefits of This Architecture

### ✅ Clean Separation via CRDs
- Certificate configuration is declarative
- Uses AWS LBC's native `LoadBalancerConfiguration` CRD
- No annotation hacks

### ✅ Follows Kubernetes Patterns
- Declarative configuration (Gateway + LoadBalancerConfiguration)
- Single responsibility (AWS LBC manages infrastructure)
- Clean separation of concerns

### ✅ Avoids All Race Conditions
- No competition with AWS LBC
- No conflicting AWS API calls
- Single source of truth (LoadBalancerConfiguration resource)

### ✅ Uses Well-Known Constants
- ALB hosted zone IDs are public AWS constants
- No API calls needed to discover them
- Deterministic and fast

## Code Locations

- **Gateway pool creation**: `internal/gateway/pool.go:CreateGateway()`
- **LoadBalancerConfiguration sync**: `internal/controller/loadbalancerconfig.go`
- **Certificate collection**: `internal/controller/loadbalancerconfig.go:getGatewayCertificateARNs()`
- **Route53 ALIAS creation**: `internal/controller/gateway.go:ensureRoute53Alias()`
- **Region extraction**: `internal/aws/regions.go:ExtractRegionFromALBDNS()`
- **Hosted zone lookup**: `internal/aws/regions.go:GetALBHostedZoneID()`

## Testing Considerations

In tests, mock the Gateway status updates to simulate AWS LBC behavior:

```go
// Simulate AWS LBC provisioning the ALB
gw.Status.Addresses = []gwapiv1.GatewayStatusAddress{
    {
        Type:  ptr(gwapiv1.HostnameAddressType),
        Value: "k8s-edge-gw01-abc123.eu-west-1.elb.amazonaws.com",
    },
}
```

## References

- [AWS ALB Hosted Zone IDs](https://docs.aws.amazon.com/general/latest/gr/elb.html)
- [AWS LBC LoadBalancerConfiguration](https://kubernetes-sigs.github.io/aws-load-balancer-controller/latest/guide/gateway/loadbalancerconfig/)
- [Gateway API Spec](https://gateway-api.sigs.k8s.io/)
- [Route53 ALIAS Records](https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/resource-record-sets-choosing-alias-non-alias.html)
