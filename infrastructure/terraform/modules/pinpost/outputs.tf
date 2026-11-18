# Outputs. `site_bucket` is what the admin/demo `aws s3 sync` targets.

output "site_url" {
  value = "https://${try(var.domain_names[0], aws_cloudfront_distribution.site.domain_name)}"
}

output "cloudfront_domain_name" {
  description = "CNAME target for the custom domains."
  value       = aws_cloudfront_distribution.site.domain_name
}

output "api_function_url" {
  value = aws_lambda_function_url.api.function_url
}

output "table_name" {
  value = aws_dynamodb_table.table.name
}

output "attachments_bucket" {
  value = aws_s3_bucket.attachments.bucket
}

output "site_bucket" {
  value = aws_s3_bucket.site.bucket
}

output "distribution_id" {
  description = "Distribution to invalidate after syncing the site bucket."
  value       = aws_cloudfront_distribution.site.id
}
