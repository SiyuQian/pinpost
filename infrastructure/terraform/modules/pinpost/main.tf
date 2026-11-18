# Pinpost — CloudFront + Lambda + DynamoDB single table + S3 attachments.
#
# Everything here is on-demand: no VPC, no NAT gateway, no load balancer, no
# always-on instance.
#
# Split by resource type: dynamodb.tf, s3.tf, lambda.tf, iam.tf, cloudfront.tf,
# variables.tf and outputs.tf. This file holds only the values every other file
# reads. The provider and the backend belong to the environment root that calls
# this module — see ../../example.

terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
      # CloudFront certificates only exist in us-east-1; the root passes it in.
      configuration_aliases = [aws.us_east_1]
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.0"
    }
  }
}

data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}
data "aws_region" "current" {}

locals {
  name             = var.stack_name
  has_domains      = length(var.domain_names) > 0
  attachments_name = "${var.stack_name}-attachments-${data.aws_caller_identity.current.account_id}"
  attachments_arn  = "arn:${data.aws_partition.current.partition}:s3:::${local.attachments_name}"

  adapter_layer_arn = var.adapter_layer_arn != "" ? var.adapter_layer_arn : "arn:${data.aws_partition.current.partition}:lambda:${data.aws_region.current.region}:753240598075:layer:LambdaAdapterLayerX86:25"

  # infrastructure/build.sh writes one set of zips for every environment — the
  # binary does not vary by environment, only its configuration does.
  dist = "${path.root}/../dist"

  # ACM's validation records, keyed by the hostname they belong to. The keys
  # come from var.domain_names so they are known at plan time; only the values
  # wait on the certificate.
  cert_validation = {
    for name in var.domain_names : name => one([
      for dvo in try(aws_acm_certificate.site[0].domain_validation_options, []) : dvo
      if dvo.domain_name == name
    ])
  }

  common_env = {
    PINPOST_TABLE  = aws_dynamodb_table.table.name
    PINPOST_BUCKET = local.attachments_name
  }
}
