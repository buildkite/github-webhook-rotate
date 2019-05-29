package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/url"
	"path"
	"strings"

	"github.com/Songmu/prompter"
	"github.com/buildkite/cli/git"
	"github.com/buildkite/cli/graphql"
	"github.com/fatih/color"
	"github.com/google/go-github/v25/github"
	"golang.org/x/oauth2"
)

const (
	githubRepositoryProvider = `RepositoryProviderGithub`
)

func main() {
	org := flag.String("buildkite-org", "", "The buildkite organization")
	graphqlToken := flag.String("graphql-token", "", "A graphql token")
	githubToken := flag.String("github-token", "", "A GitHub personal access token")
	prompt := flag.Bool("prompt", true, "Whether to prompt before each rotate")
	pipeline := flag.String("pipeline", "", "A specific pipeline slug to rotate")

	flag.Parse()
	log.SetFlags(log.Ltime)

	ctx := context.Background()

	// set up a client for buildkite's graphql api
	client, err := graphql.NewClient(*graphqlToken)
	if err != nil {
		log.Fatal(err)
	}

	// set up a client for github's api, requires a key with `admin:repo_hook`
	ghClient := github.NewClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: *githubToken},
	)))

	// ---------------------------------------------------------
	// build up a map of buildkite webhook -> (github repository + hook)

	repoHookMap := map[string][]githubRepositoryHook{}

	log.Printf("Building a map of github repositories with buildkite webhooks for %s", *org)

	pipelines, err := listGithubPipelines(client, *org, *pipeline)
	if err != nil {
		log.Fatalf(color.RedString("🚨 Error getting pipelines: %v"), err)
	}

	repoHooks := map[string][]*github.Hook{}

	// iterate over all out pipelines
	for _, pipeline := range pipelines {
		// don't process repositories multiple times
		if _, ok := repoHooks[pipeline.Repository.String()]; ok {
			continue
		}

		log.Printf("Finding webhooks for https://github.com/%s", pipeline.Repository.String())

		hooks, err := getGithubRepositoryWebhooks(ctx, ghClient, pipeline.Repository)
		if err != nil {
			log.Fatalf(color.RedString("🚨 Error getting webhooks for https://buildkite.com/%s: %v"),
				pipeline.String(), err)
		}

		// store all the matching webhooks in our map
		for _, hook := range hooks {
			hookURL := hook.Config["url"].(string)

			// extract just the token to allow format changes over time
			hookToken, err := getWebhookToken(hookURL)
			if err != nil {
				log.Fatalf(color.RedString("🚨 Error parsing webhook: %v"), err)
			}

			if _, exists := repoHookMap[hookToken]; !exists {
				repoHookMap[hookToken] = []githubRepositoryHook{
					githubRepositoryHook{pipeline.Repository, hook},
				}
			} else {
				repoHookMap[hookToken] = append(repoHookMap[hookToken],
					githubRepositoryHook{pipeline.Repository, hook})
			}
		}

		// track the hooks for this repository
		repoHooks[pipeline.Repository.String()] = hooks
	}

	// ---------------------------------------------------------------
	// iterate over pipelines and map webhook to github repositories

	fmt.Println()

	for _, pipeline := range pipelines {
		fmt.Printf("Pipeline: http://buildkite.com/%s/%s\n", pipeline.Org, pipeline.Slug)
		fmt.Printf("\tCurrent Webhook: %s\n", pipeline.WebhookURL)

		// lookup repositories that refer to this webhook token
		matches, ok := repoHookMap[pipeline.WebhookToken]
		if !ok {
			fmt.Printf(color.YellowString("\t⚠️  No GitHub repositories with matching hooks\n"))
		} else {
			fmt.Printf("\tMatching GitHub Repositories:\n")
		}

		// show repositories that match the pipeline webhook
		for _, match := range matches {
			fmt.Printf("\t\thttps://github.com/%s\n", match.githubRepository.String())
			fmt.Printf("\t\t\tUpdate https://github.com/%s/settings/hooks/%d\n",
				match.githubRepository.String(), *match.Hook.ID)
		}

		// show unknown webhooks for the repository
		if hooks, ok := repoHooks[pipeline.Repository.String()]; ok {
			unknown := []*github.Hook{}
			for _, hook := range hooks {
				if !isHookReferencedInPipelines(hook, pipelines) {
					unknown = append(unknown, hook)
				}
			}
			if len(unknown) > 0 {
				fmt.Printf(color.YellowString("\t⚠️  Unknown Buildkite hooks found\n"))
				for _, hook := range unknown {
					fmt.Printf("\t\thttps://github.com/%s\n", pipeline.Repository.String())
					fmt.Printf("\t\t\thttps://github.com/%s/settings/hooks/%d\n",
						pipeline.Repository.String(), *hook.ID)
					fmt.Printf("\t\t\t\t%s\n", hook.Config["url"])
				}
			}
		}

		if *prompt {
			fmt.Println()

			if apply := prompter.YN("Rotate webhook?", true); !apply {
				continue
			}
		}

		fmt.Println()

		if len(matches) > 0 {
			// first off try updating it to the current value as a test
			err = updateGithubRepositoryHook(ctx, ghClient, matches[0], pipeline.WebhookURL)
			if err != nil {
				log.Fatalf(color.RedString(
					"🚨 Can't update repository webhooks, permissions perhaps? %v", err))
			}

			log.Printf("Successfully tested updating github webhook")
		}

		newWebhookURL, err := rotateBuildkiteWebhook(client, pipeline.ID)
		if err != nil {
			log.Fatalf(color.RedString(
				"🚨 Error rotating buildkite webhooks: %v", err))
		}

		log.Printf("New buildkite webhook is %s", newWebhookURL)

		// apply the new webhook to all the matching repository hooks
		for _, match := range matches {
			log.Printf("Updating https://github.com/%s/settings/hooks/%d",
				match.githubRepository.String(), *match.Hook.ID)
			err = updateGithubRepositoryHook(ctx, ghClient, match, newWebhookURL)
			if err != nil {
				log.Fatalf(color.RedString(
					"🚨 Error updating github webhook: %v", err))
			}
		}

		fmt.Printf(color.GreenString("\nUpdated webhook ✅\n\n"))
	}
}

type githubRepositoryHook struct {
	githubRepository
	*github.Hook
}

type githubRepository struct {
	Org    string
	Name   string
	Remote string
}

func (r githubRepository) String() string {
	return fmt.Sprintf("%s/%s", r.Org, r.Name)
}

func parseGithubRepository(gitRemote string) (githubRepository, error) {
	u, err := git.ParseGittableURL(gitRemote)
	if err != nil {
		return githubRepository{}, err
	}

	pathParts := strings.SplitN(strings.TrimLeft(strings.TrimSuffix(u.Path, ".git"), "/"), "/", 2)

	if len(pathParts) < 2 {
		return githubRepository{}, fmt.Errorf("Failed to parse remote %q", gitRemote)
	}

	return githubRepository{pathParts[0], pathParts[1], gitRemote}, nil
}

// Webhook formats over the years
// https://webhook.buildbox.io/github/xxxxxxxxxxxxxxxxx
// https://webhook.buildkite.com/github/xxxxxxxxxxxxxxxxx
// https://webhook.buildkite.com/deliver/xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx

func getWebhookToken(webhookURL string) (string, error) {
	u, err := url.Parse(webhookURL)
	if err != nil {
		return "", err
	}
	return path.Base(u.Path), nil
}

func isHookReferencedInPipelines(hook *github.Hook, pipelines []pipeline) bool {
	token, err := getWebhookToken(hook.Config["url"].(string))
	if err != nil {
		return false
	}
	for _, pipeline := range pipelines {
		if pipeline.WebhookToken == token {
			return true
		}
	}
	return false
}

func getGithubRepositoryWebhooks(ctx context.Context, client *github.Client, repo githubRepository) ([]*github.Hook, error) {
	hooks, _, err := client.Repositories.ListHooks(ctx, repo.Org, repo.Name, &github.ListOptions{})
	if err != nil {
		return nil, err
	}

	var buildkiteHooks []*github.Hook

	for _, hook := range hooks {
		webhookURL, ok := hook.Config["url"].(string)
		if ok && strings.Contains(webhookURL, "webhook.buildbox.io") ||
			strings.Contains(webhookURL, "webhook.buildkite.com") {
			buildkiteHooks = append(buildkiteHooks, hook)
		}
	}

	return buildkiteHooks, nil
}

func updateGithubRepositoryHook(ctx context.Context, client *github.Client, repoHook githubRepositoryHook, hook string) error {
	// https://developer.github.com/v3/repos/hooks/#edit-a-hook
	_, _, err := client.Repositories.EditHook(ctx, repoHook.Org, repoHook.Name, *repoHook.Hook.ID, &github.Hook{
		Config: map[string]interface{}{
			"url": github.String(hook),
		},
	})
	return err
}

type pipeline struct {
	ID           string
	Org          string
	Slug         string
	URL          string
	WebhookURL   string
	WebhookToken string
	Repository   githubRepository
}

func (p pipeline) String() string {
	return fmt.Sprintf("%s/%s", p.Org, p.Slug)
}

func listGithubPipelines(client *graphql.Client, org, pipelineFilter string) ([]pipeline, error) {
	resp, err := client.Do(`
	query ListPipelines($org: ID!) {
		organization(slug: $org) {
			slug
			pipelines(first: 500) {
				edges {
					node {
						id
						slug
						url
						repository {
							provider {
								__typename
								webhookUrl
							}
							url
						}
					}
				}
			}
		}
	}
	`, map[string]interface{}{
		`org`: org,
	})
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		body, _ := ioutil.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s - %s", resp.Status, string(body))
	}

	var parsedResp struct {
		Data struct {
			Organization struct {
				Slug      string `json:"slug"`
				Pipelines struct {
					Edges []struct {
						Node struct {
							ID         string `json:"id"`
							Slug       string `json:"slug"`
							URL        string `json:"url"`
							Repository struct {
								Provider struct {
									TypeName   string `json:"__typename"`
									WebhookURL string `json:"webhookUrl"`
								} `json:"provider"`
								URL string `json:"url"`
							} `json:"repository"`
						} `json:"node"`
					} `json:"edges"`
				} `json:"pipelines"`
			} `json:"organization"`
		} `json:"data"`
	}

	if err = resp.DecodeInto(&parsedResp); err != nil {
		return nil, fmt.Errorf("Failed to parse GraphQL response: %v", err)
	}

	var pipelines []pipeline
	for _, pipelineEdge := range parsedResp.Data.Organization.Pipelines.Edges {
		if pipelineFilter != "" && pipelineEdge.Node.Slug != pipelineFilter {
			continue
		}
		if pipelineEdge.Node.Repository.Provider.TypeName != githubRepositoryProvider {
			continue
		}
		repo, err := parseGithubRepository(pipelineEdge.Node.Repository.URL)
		if err != nil {
			return nil, err
		}
		webhookToken, err := getWebhookToken(pipelineEdge.Node.Repository.Provider.WebhookURL)
		if err != nil {
			return nil, err
		}
		pipelines = append(pipelines, pipeline{
			ID:           pipelineEdge.Node.ID,
			URL:          pipelineEdge.Node.URL,
			Org:          org,
			Slug:         pipelineEdge.Node.Slug,
			WebhookURL:   pipelineEdge.Node.Repository.Provider.WebhookURL,
			WebhookToken: webhookToken,
			Repository:   repo,
		})
	}
	return pipelines, nil
}

func rotateBuildkiteWebhook(client *graphql.Client, pipelineID string) (string, error) {
	resp, err := client.Do(`
		mutation($input: PipelineRotateWebhookURLInput!) {
			pipelineRotateWebhookURL(input: $input) {
				pipeline {
					webhookURL
				}
			}
		}
	`, map[string]interface{}{
		"input": map[string]interface{}{
			"id": pipelineID,
		}})
	if err != nil {
		return "", err
	}

	var parsedResp struct {
		Data struct {
			PipelineRotateWebhookURL struct {
				Pipeline struct {
					WebhookURL string `json:"webhookURL"`
				} `json:"pipeline"`
			} `json:"pipelineRotateWebhookURL"`
		} `json:"data"`
	}

	if err = resp.DecodeInto(&parsedResp); err != nil {
		return "", fmt.Errorf("Failed to parse GraphQL response: %v", err)
	}

	return parsedResp.Data.PipelineRotateWebhookURL.Pipeline.WebhookURL, nil
}
