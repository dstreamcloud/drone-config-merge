package main

import (
	"bytes"
	"context"
	"github.com/drone/drone-go/drone"
	"github.com/drone/drone-go/plugin/config"
	"github.com/google/go-github/github"
	"github.com/kelseyhightower/envconfig"
	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
	"gopkg.in/yaml.v2"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

type Config struct {
	Secret      string `envconfig:"DRONE_YAML_SECRET"`
	Addr        string `envconfig:"DRONE_PLUGIN_ADDR"`
	GithubToken string `envconfig:"DRONE_PLUGIN_GITHUB_TOKEN"`
}

type plugin struct {
	client *github.Client
}

func (p *plugin) Find(ctx context.Context, req *config.Request) (*drone.Config, error) {
	println(req.Repo.Config)
	decoder := yaml.NewDecoder(strings.NewReader(req.Repo.Config))
	var records []map[string]interface{}
	var dependsOn []string
	for {
		record := map[string]interface{}{}
		if err := decoder.Decode(&record); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if record["kind"] == "virtual-pipeline" {
			pipelines, ok := record["pipelines"].([]string)
			if !ok {
				continue
			}

			for _, k := range pipelines {
				dependsOn = append(dependsOn, k)
				// TODO parallelism of fetching drone.yml
				content, _, _, err := p.client.Repositories.GetContents(ctx, req.Repo.Namespace, req.Repo.Name, filepath.Join(k, ".drone.yml"), &github.RepositoryContentGetOptions{Ref: req.Build.Ref})
				if err != nil {
					return nil, err
				}

				body := *content.Content
				record := map[string]interface{}{}
				if err := yaml.Unmarshal([]byte(body), &record); err != nil {
					return nil, err
				}

				records = append(records, record)
			}
			continue
		}
		records = append(records, record)
	}

	output := bytes.NewBuffer(nil)
	encoder := yaml.NewEncoder(output)
	for _, record := range records {
		if record["injectDependencies"] == true {
			record["depends_on"] = dependsOn
		}
		if err := encoder.Encode(&record); err != nil {
			return nil, err
		}
	}

	return &drone.Config{
		Data: output.String(),
	}, nil
}

func main() {
	cfg := &Config{}
	envconfig.MustProcess("", cfg)
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: cfg.GithubToken},
	)
	tc := oauth2.NewClient(context.Background(), ts)
	p := &plugin{
		client: github.NewClient(tc),
	}
	handler := config.Handler(p, cfg.Secret, logrus.StandardLogger())
	http.Handle("/", handler)
	logrus.Fatal(http.ListenAndServe(cfg.Addr, nil))
}
