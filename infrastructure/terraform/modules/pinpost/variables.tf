# Module inputs. Only stack_name is required; an environment root passes the
# rest when it differs from the default.

variable "stack_name" {
  type        = string
  description = "Resource name prefix. Bucket names are lowercase-only, so keep it lowercase."

  validation {
    condition     = can(regex("^[a-z0-9-]+$", var.stack_name))
    error_message = "stack_name must be lowercase letters, digits and hyphens."
  }
}

variable "adapter_layer_arn" {
  type        = string
  default     = ""
  description = <<-EOT
    AWS Lambda Web Adapter layer for the provider's region; defaults to version
    25 of the public x86 layer. It turns the Lambda invocation into an ordinary HTTP
    request to the Go server on :8080, which is why cmd/server needs no
    Lambda-specific code.
  EOT
}

variable "google_client_id" {
  type        = string
  default     = ""
  description = "Google OAuth web client id. Empty disables Google sign-in."
}

variable "google_allowed_emails" {
  type        = list(string)
  default     = []
  description = <<-EOT
    The only Google addresses allowed to sign in. An allowed address gets a
    member record on first sign-in; the first entry also seeds the owner on an
    empty table when no seed_admin_email is set.
  EOT
}

variable "seed_admin_email" {
  type        = string
  default     = ""
  description = "Provisions the first owner on an empty table (ignored afterwards)."
}

variable "seed_admin_password" {
  type      = string
  default   = ""
  sensitive = true
}

variable "cors_allowed_origins" {
  type        = string
  default     = ""
  description = "Comma-separated origin allowlist for the public ingestion endpoints."
}

variable "domain_names" {
  type        = list(string)
  default     = []
  description = <<-EOT
    Custom hostnames for the distribution — admin console first, API second.
    Empty keeps the default *.cloudfront.net domain and certificate. Must all
    be inside the Cloudflare zone below.
  EOT
}

variable "cloudflare_zone_id" {
  type        = string
  default     = ""
  description = "Cloudflare zone ID holding domain_names. Only read when domain_names is set."
}
