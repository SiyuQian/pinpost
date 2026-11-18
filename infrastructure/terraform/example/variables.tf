variable "region" {
  description = "AWS region for the stack."
  type        = string
  default     = "us-east-1"
}

variable "stack_name" {
  description = "Prefix for every resource name, e.g. pinpost-stg or pinpost."
  type        = string
  default     = "pinpost"
}

variable "domain_names" {
  description = "Custom hostnames, admin console first, API second. Empty keeps the *.cloudfront.net domain."
  type        = list(string)
  default     = []
}

variable "google_client_id" {
  type        = string
  default     = ""
  description = "Google OAuth web client id for the admin dashboard."
}

variable "google_allowed_emails" {
  type        = list(string)
  default     = []
  description = "The only Google addresses allowed to sign in to the dashboard."
}

variable "seed_admin_email" {
  description = "Provisions the first owner on an empty table (ignored afterwards)."
  type        = string
  default     = ""
}

variable "seed_admin_password" {
  description = "Password for that first owner."
  type        = string
  default     = ""
  sensitive   = true
}

variable "cors_allowed_origins" {
  description = "Comma-separated origin allowlist for the public ingestion endpoints."
  type        = string
  default     = ""
}

variable "cloudflare_zone_id" {
  description = "Cloudflare zone ID holding domain_names (Overview -> API -> Zone ID). Passed via TF_VAR_cloudflare_zone_id."
  type        = string
  default     = ""
}
