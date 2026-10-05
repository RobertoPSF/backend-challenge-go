#!/bin/sh

set -eu

REGION="${AWS_DEFAULT_REGION:-us-east-1}"
MAX_RECEIVE_COUNT="${SQS_MAX_RECEIVE_COUNT:-5}"

create_fifo() {
  awslocal sqs create-queue --region "$REGION" --queue-name "$1" \
    --attributes FifoQueue=true,ContentBasedDeduplication=false,VisibilityTimeout=30 \
    --query QueueUrl --output text
}

queue_arn() {
  awslocal sqs get-queue-attributes --region "$REGION" --queue-url "$1" \
    --attribute-names QueueArn --query Attributes.QueueArn --output text
}

with_dlq() {
  queue_url="$(create_fifo "$1")"
  dlq_arn="$(queue_arn "$(create_fifo "$2")")"
  awslocal sqs set-queue-attributes --region "$REGION" --queue-url "$queue_url" \
    --attributes "{\"RedrivePolicy\":\"{\\\"deadLetterTargetArn\\\":\\\"$dlq_arn\\\",\\\"maxReceiveCount\\\":\\\"$MAX_RECEIVE_COUNT\\\"}\"}"
  echo "queue $1 -> dlq $2 (maxReceiveCount=$MAX_RECEIVE_COUNT)"
}

with_dlq wager-transactions.fifo wager-transactions-dlq.fifo
with_dlq wallet-events.fifo wallet-events-dlq.fifo

# Marcador usado pelo healthcheck do compose: só existe depois que tudo foi criado.
touch /tmp/queues-ready
