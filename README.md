# Buildkite Webhook Rotate for Github

This is a utility for rotating the secret webhooks used for triggering builds in a Buildkite Pipeline.

Whilst these webhooks verify the source address belongs to GitHub, if they are disclosed we recommend rotation.

This tools requires a Github Personal Access Token with `admin:repo_hook` and a Buildkite GraphQL API token.

## Installation

This uses Go 1.12 with modules enabled.

```
go get -u github.com/buildkite/github-webhook-rotate
```

## Running

By default the tool will prompt before each change that is made.

```
export GRAPHQL_TOKEN=....
export GITHUB_TOKEN=...

github-webhook-rotate --buildkite-org=<my-org> --graphql-token "$GRAPHQL_TOKEN" --github-token "$GITHUB_TOKEN"
```
