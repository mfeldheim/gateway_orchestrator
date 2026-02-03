# Certificate Management with AWS Load Balancer Controller

## Overview

This document explains how the Gateway Orchestrator integrates with AWS Load Balancer Controller for certificate management.

## Key Architecture Principle

**We only manage three things:**
1. **Gateway resources** (Kubernetes API)
2. **Route53 DNS records** (AWS Route53 API)
3. **ACM certificates** (AWS ACM API)

**AWS Load Balancer Controller manages everything else** (ALB provisioning, listener configuration, certificate attachment).

## How It Works

### 1. Gateway Creation

When creating a Gateway, we add AWS Load Balancer Controller annotations:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: gw-01
  namespace: edge
  annotations:
    # AWS Load Balancer Controller annotations
    alb.ingress.kubernetes.io/scheme: internet-facing
    alb.ingress.kubernetes.io/target-type: ip
    
    # Our tracking annotations
    gateway.opendi.com/visibility: internet-facing
    gateway.opendi.com/certificate-count: "0"
spec:
  gatewayClassName: aws-alb
  listeners:
    - name: https
      protocol: HTTPS
      port: 443
    - name: http
      protocol: HTTP
      port: 80
```

**AWS Load Balancer Controller does:**
- Provisions an ALB in AWS
- Configures listeners (HTTP:80, HTTPS:443)
- Updates Gateway status with LoadBalancer address

### 2. Certificate Attachment

When a `GatewayHostnameRequest` is reconciled and an ACM certificate is issued:

**We do:**
```go
// Add certificate ARN to Gateway annotations
gw.Annotations["alb.ingress.kubernetes.io/certificate-arn"] = certArn
gw.Annotations["gateway.opendi.com/certificate-count"] = "1"

// Update the Gateway resource
r.Update(ctx, gw)
```

**AWS Load Balancer Controller does:**
- Watches for Gateway annotation changes
- Reads `alb.ingress.kubernetes.io/certificate-arn`
- Calls ELBv2 API to attach certificate to ALB HTTPS listener
- Handles SNI configuration for multiple certificates

### 3. Route53 ALIAS Record Creation

**We do:**
```go
// 1. Get LoadBalancer DNS from Gateway status (populated by AWS LBC)
lbDNS := gw.Status.Addresses[0].Value
// Example: k8s-edge-gw01-abc123.us-east-1.elb.amazonaws.com

// 2. Extract region from DNS name
region := aws.ExtractRegionFromALBDNS(lbDNS)  // "us-east-1"

// 3. Look up well-known ALB hosted zone ID for the region
hostedZoneID := aws.GetALBHostedZoneID(region)  // "Z35SXDOTRQ7X7K"

// 4. Create Route53 ALIAS record
record := aws.DNSRecord{
    Name: "test.opendi.com",
    Type: "A",
    AliasTarget: &aws.AliasTarget{
        DNSName:      lbDNS,
        HostedZoneID: hostedZoneID,  // ALB's canonical zone
    },
}
route53Client.CreateOrUpdateRecord(ctx, userZoneId, record)
```

**Why we don't need ELBv2 API:**
- ALB canonical hosted zone IDs are **well-known public values** per region
- We can extract the region from the LoadBalancer DNS name
- No need to query ELBv2 `DescribeLoadBalancers` API

### ALB Canonical Hosted Zone IDs by Region

These are constants provided by AWS:

| Region | Hosted Zone ID |
|--------|---------------|
| us-east-1 | Z35SXDOTRQ7X7K |
| us-east-2 | Z3AADJGX6KTTL2 |
| us-west-1 | Z368ELLRRE2KJ0 |
| us-west-2 | Z1H1FL5HABSF5 |
| eu-west-1 | Z32O12XQLNTSW2 |
| eu-central-1 | Z215JYRZR1TBD5 |
| ap-southeast-1 | Z1LMS91P8CMLE5 |
| ... | (see `internal/aws/regions.go`) |

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
[5] Update Gateway Annotations ← WE DO THIS
    (add certificate-arn)
         ↓
    AWS Load Balancer Controller ← AWS LBC DOES THIS
         ↓
    Provisions ALB + Attaches Certificate
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
✅ Gateway resources (create, update annotations)  
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

### ✅ Maximum Simplicity
- Only 2 AWS service clients needed (ACM, Route53)
- No ELBv2 client required
- Fewer dependencies, less code

### ✅ Follows Kubernetes Patterns
- Declarative configuration (Gateway resources)
- Single responsibility (AWS LBC manages infrastructure)
- Clean separation of concerns

### ✅ Avoids All Race Conditions
- No competition with AWS LBC
- No conflicting AWS API calls
- Single source of truth (Gateway resource)

### ✅ Uses Well-Known Constants
- ALB hosted zone IDs are public AWS constants
- No API calls needed to discover them
- Deterministic and fast

## Code Locations

- **Gateway pool creation**: `internal/gateway/pool.go:CreateGateway()`
- **Certificate attachment**: `internal/controller/gateway.go:attachCertificateToGateway()`
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
        Value: "k8s-edge-gw01-abc123.us-east-1.elb.amazonaws.com",
    },
}
```

## References

- [AWS ALB Hosted Zone IDs](https://docs.aws.amazon.com/general/latest/gr/elb.html)
- [AWS Load Balancer Controller Annotations](https://kubernetes-sigs.github.io/aws-load-balancer-controller/latest/guide/ingress/annotations/)
- [Gateway API Spec](https://gateway-api.sigs.k8s.io/)
- [Route53 ALIAS Records](https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/resource-record-sets-choosing-alias-non-alias.html)
