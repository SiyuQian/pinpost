provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Environment = var.stack_name
      Project     = "pinpost"
      ManagedBy   = "terraform"
    }
  }
}

# CloudFront reads its certificate from us-east-1 only. Nothing else uses this.
provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"
}

# Only used when domain_names is set. The provider reads its API token from
# CLOUDFLARE_API_TOKEN — a secret in CI, exported locally otherwise. Never
# checked in.
provider "cloudflare" {}
