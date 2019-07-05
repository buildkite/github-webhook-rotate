# Buildkite Webhook Rotate for Github

This is a utility for rotating the secret webhooks used for triggering builds in a Buildkite Pipeline.

Whilst these webhooks verify the source address belongs to GitHub, if they are disclosed we recommend rotation.

This tools requires a Github Personal Access Token with `admin:repo_hook` and a Buildkite GraphQL API token.

## Installation

This uses Go 1.12 with modules enabled.

```shell
go get -u github.com/buildkite/github-webhook-rotate
```

## Running

By default the tool will prompt before each change that is made.

```shell
export GRAPHQL_TOKEN="...."
export GITHUB_TOKEN="..."

github-webhook-rotate \
  --buildkite-org="<my-org>" \
  --graphql-token "$GRAPHQL_TOKEN" \
  --github-token "$GITHUB_TOKEN"
```

## How it works

* Enumerate all Buildkite pipelines via GraphQL
* For each Pipeline, infer the GitHub repository
* For each GitHub Repository, enumerate Buildkite hooks and build a mapping
* For each Pipeline
  * Rotate the build webhook with the `pipelineRotateWebhookURL` GraphQL mutation
  * Update all Github Repository webhooks that refer to the updated webhook

## Copyright

Copyright (c) 2019 Buildkite Pty Ltd. See [LICENSE](./LICENSE.txt) for details.
