# One role per function. SAM's DynamoDBCrudPolicy and its implicit event-source
# grants are macros with no Terraform equivalent, so they are spelled out.

data "aws_iam_policy_document" "lambda_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

# The SAM DynamoDBCrudPolicy, spelled out.
data "aws_iam_policy_document" "table_crud" {
  statement {
    actions = [
      "dynamodb:GetItem",
      "dynamodb:PutItem",
      "dynamodb:UpdateItem",
      "dynamodb:DeleteItem",
      "dynamodb:Query",
      "dynamodb:Scan",
      "dynamodb:BatchGetItem",
      "dynamodb:BatchWriteItem",
      "dynamodb:ConditionCheckItem",
      "dynamodb:DescribeTable",
    ]
    resources = [aws_dynamodb_table.table.arn]
  }
  statement {
    actions   = ["dynamodb:Query"]
    resources = ["${aws_dynamodb_table.table.arn}/index/*"]
  }
}

resource "aws_iam_role" "api" {
  name               = "${local.name}-api"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
}

data "aws_iam_policy_document" "api" {
  source_policy_documents = [data.aws_iam_policy_document.table_crud.json]

  # Presigning issues no S3 call, but a presigned URL is only valid if this
  # role actually holds the action — the API presigns both the upload PUT and
  # the download GET, so it needs both.
  statement {
    actions   = ["s3:PutObject", "s3:GetObject"]
    resources = ["${local.attachments_arn}/*"]
  }
}

resource "aws_iam_role_policy" "api" {
  role   = aws_iam_role.api.id
  policy = data.aws_iam_policy_document.api.json
}

resource "aws_iam_role" "scan" {
  name               = "${local.name}-scan"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
}

data "aws_iam_policy_document" "scan" {
  source_policy_documents = [data.aws_iam_policy_document.table_crud.json]
  statement {
    actions   = ["s3:GetObject"]
    resources = ["${local.attachments_arn}/*"]
  }
}

resource "aws_iam_role_policy" "scan" {
  role   = aws_iam_role.scan.id
  policy = data.aws_iam_policy_document.scan.json
}

resource "aws_iam_role" "webhook" {
  name               = "${local.name}-webhook"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
}

data "aws_iam_policy_document" "webhook" {
  source_policy_documents = [data.aws_iam_policy_document.table_crud.json]

  # Reading the change stream, and the event source mapping's on-failure
  # destination. SAM attached both implicitly; Terraform does not.
  statement {
    actions = [
      "dynamodb:DescribeStream",
      "dynamodb:GetRecords",
      "dynamodb:GetShardIterator",
      "dynamodb:ListStreams",
    ]
    resources = [aws_dynamodb_table.table.stream_arn]
  }
  statement {
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.dead_letter.arn]
  }
}

resource "aws_iam_role_policy" "webhook" {
  role   = aws_iam_role.webhook.id
  policy = data.aws_iam_policy_document.webhook.json
}

resource "aws_iam_role_policy_attachment" "logs" {
  for_each   = { api = aws_iam_role.api.name, scan = aws_iam_role.scan.name, webhook = aws_iam_role.webhook.name }
  role       = each.value
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}
