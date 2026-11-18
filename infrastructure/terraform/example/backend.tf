# State lives in an S3 bucket you create once by hand (see the README), so a
# `terraform destroy` can never delete the state it is writing to. Replace the
# bucket name and region with your own.
terraform {
  backend "s3" {
    bucket       = "REPLACE-ME-pinpost-tfstate"
    key          = "example/terraform.tfstate"
    region       = "us-east-1"
    use_lockfile = true
    encrypt      = true
  }
}
