output "site_url" {
  description = "CloudFront entry point for the admin console and the demo."
  value       = module.pinpost.site_url
}

output "api_function_url" {
  description = "Lambda Function URL behind the /v1/* cache behavior."
  value       = module.pinpost.api_function_url
}

output "table_name" {
  description = "DynamoDB single table."
  value       = module.pinpost.table_name
}

output "attachments_bucket" {
  description = "Bucket the SDK PUTs attachment bytes into."
  value       = module.pinpost.attachments_bucket
}

output "site_bucket" {
  description = "Bucket the admin console and demo are synced to."
  value       = module.pinpost.site_bucket
}

output "cloudfront_domain_name" {
  description = "Distribution the custom-domain CNAMEs point at."
  value       = module.pinpost.cloudfront_domain_name
}

output "distribution_id" {
  description = "Distribution the site sync invalidates."
  value       = module.pinpost.distribution_id
}
