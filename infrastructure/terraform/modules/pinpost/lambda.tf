# The three functions, their log groups and the two event sources that drive
# them: S3 object-created into scan, and the table's change stream into webhook.

resource "aws_lambda_function" "api" {
  function_name    = "${local.name}-api"
  role             = aws_iam_role.api.arn
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  handler          = "bootstrap"
  memory_size      = 256
  timeout          = 30
  filename         = "${local.dist}/server.zip"
  source_code_hash = try(filebase64sha256("${local.dist}/server.zip"), null)
  layers           = [local.adapter_layer_arn]

  environment {
    variables = merge(local.common_env, {
      AWS_LWA_PORT         = "8080"
      ADDR                 = ":8080"
      CORS_ALLOWED_ORIGINS = var.cors_allowed_origins

      # Who may sign in to the dashboard. The client id is public; the
      # allowlist is the access control.
      PINPOST_GOOGLE_CLIENT_ID      = var.google_client_id
      PINPOST_GOOGLE_ALLOWED_EMAILS = join(",", var.google_allowed_emails)
      }, var.seed_admin_email == "" ? {} : {
      PINPOST_ADMIN_EMAIL    = var.seed_admin_email
      PINPOST_ADMIN_PASSWORD = var.seed_admin_password
    })
  }

  depends_on = [aws_cloudwatch_log_group.api]
}

resource "aws_lambda_function_url" "api" {
  function_name      = aws_lambda_function.api.function_name
  authorization_type = "NONE"
}

resource "aws_lambda_function" "scan" {
  function_name    = "${local.name}-scan"
  role             = aws_iam_role.scan.arn
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  handler          = "bootstrap"
  memory_size      = 256
  timeout          = 30
  filename         = "${local.dist}/scan.zip"
  source_code_hash = try(filebase64sha256("${local.dist}/scan.zip"), null)

  environment {
    variables = local.common_env
  }

  depends_on = [aws_cloudwatch_log_group.scan]
}

resource "aws_lambda_permission" "scan_from_s3" {
  function_name  = aws_lambda_function.scan.function_name
  action         = "lambda:InvokeFunction"
  principal      = "s3.amazonaws.com"
  source_account = data.aws_caller_identity.current.account_id
  source_arn     = local.attachments_arn
}

resource "aws_lambda_function" "webhook" {
  function_name    = "${local.name}-webhook"
  role             = aws_iam_role.webhook.arn
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  handler          = "bootstrap"
  memory_size      = 256
  timeout          = 30
  filename         = "${local.dist}/webhook.zip"
  source_code_hash = try(filebase64sha256("${local.dist}/webhook.zip"), null)

  environment {
    variables = local.common_env
  }

  depends_on = [aws_cloudwatch_log_group.webhook]
}

resource "aws_lambda_event_source_mapping" "changes" {
  event_source_arn  = aws_dynamodb_table.table.stream_arn
  function_name     = aws_lambda_function.webhook.arn
  starting_position = "LATEST"
  batch_size        = 10

  # The table also holds rate-limit counters, sessions, idempotency keys and
  # this function's own delivery records. derive() drops them all — but only
  # after the invocation has been billed, so filter to the two prefixes it
  # actually handles.
  filter_criteria {
    filter {
      pattern = jsonencode({ eventName = ["INSERT", "MODIFY"], dynamodb = { Keys = { sk = { S = [{ prefix = "FB#" }] } } } })
    }
    filter {
      pattern = jsonencode({ eventName = ["INSERT"], dynamodb = { Keys = { sk = { S = [{ prefix = "ACT#" }] } } } })
    }
  }

  # HandleStream reports failures per record, so a retry re-sends only the
  # delivery that failed rather than the whole batch.
  function_response_types = ["ReportBatchItemFailures"]

  # Retries replace the old six-step backoff table in the worker, but they are
  # minutes, not the day the ladder covered: an endpoint down longer than
  # maximum_record_age_in_seconds loses those events to the dead-letter queue.
  # Nothing drains that queue yet.
  # ponytail: best-effort for ~1h, no DLQ consumer — add a replay function if
  # subscribers need the old 24h ladder back.
  maximum_retry_attempts         = 5
  maximum_record_age_in_seconds  = 3600
  bisect_batch_on_function_error = true

  destination_config {
    on_failure {
      destination_arn = aws_sqs_queue.dead_letter.arn
    }
  }
}

resource "aws_sqs_queue" "dead_letter" {
  name                      = "${local.name}-dlq"
  message_retention_seconds = 1209600
}

# Ingestion charges make retention the single most expensive knob here.
resource "aws_cloudwatch_log_group" "api" {
  name              = "/aws/lambda/${local.name}-api"
  retention_in_days = 7
}

resource "aws_cloudwatch_log_group" "scan" {
  name              = "/aws/lambda/${local.name}-scan"
  retention_in_days = 7
}

resource "aws_cloudwatch_log_group" "webhook" {
  name              = "/aws/lambda/${local.name}-webhook"
  retention_in_days = 7
}
