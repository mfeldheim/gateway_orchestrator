# AWS IAM Role and Policy for IRSA
#
# This creates an IAM role that the Gateway Orchestrator service account
# can assume via IRSA (IAM Roles for Service Accounts).

data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

# IAM policy for Gateway Orchestrator
resource "aws_iam_policy" "gateway_orchestrator" {
  name        = "GatewayOrchestratorPolicy"
  description = "Permissions for Gateway Orchestrator controller"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = [
          "acm:RequestCertificate",
          "acm:DescribeCertificate",
          "acm:DeleteCertificate",
          "acm:ListCertificates",
          "acm:AddTagsToCertificate"
        ]
        Resource = "*"
      },
      {
        Effect = "Allow"
        Action = [
          "route53:ChangeResourceRecordSets",
          "route53:ListResourceRecordSets",
          "route53:GetChange"
        ]
        Resource = [
          "arn:aws:route53:::hostedzone/*",
          "arn:aws:route53:::change/*"
        ]
      },
      {
        Effect = "Allow"
        Action = [
          "route53:ListHostedZones"
        ]
        Resource = "*"
      }
    ]
  })
}

# IAM role for IRSA
resource "aws_iam_role" "gateway_orchestrator" {
  name               = "gateway-orchestrator"
  assume_role_policy = data.aws_iam_policy_document.gateway_orchestrator_assume.json
}

# Trust policy for IRSA
data "aws_iam_policy_document" "gateway_orchestrator_assume" {
  statement {
    effect = "Allow"

    principals {
      type        = "Federated"
      identifiers = [var.oidc_provider_arn]
    }

    actions = ["sts:AssumeRoleWithWebIdentity"]

    condition {
      test     = "StringEquals"
      variable = "${replace(var.oidc_provider_arn, "/^(.*provider/)/", "")}:sub"
      values   = ["system:serviceaccount:${var.namespace}:gateway-orchestrator"]
    }

    condition {
      test     = "StringEquals"
      variable = "${replace(var.oidc_provider_arn, "/^(.*provider/)/", "")}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

# Attach policy to role
resource "aws_iam_role_policy_attachment" "gateway_orchestrator" {
  role       = aws_iam_role.gateway_orchestrator.name
  policy_arn = aws_iam_policy.gateway_orchestrator.arn
}

# Output the role ARN for use in service account annotation
output "irsa_role_arn" {
  description = "ARN of the IAM role for Gateway Orchestrator IRSA"
  value       = aws_iam_role.gateway_orchestrator.arn
}
