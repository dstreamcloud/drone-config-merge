package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"

	"github.com/drone/drone-go/drone"
	"github.com/drone/drone-go/plugin/config"
	"github.com/google/go-github/github"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v2"
)

type Plugin struct {
	client *github.Client
}

func New(client *github.Client) *Plugin {
	return &Plugin{client: client}
}

func (p *Plugin) Find(ctx context.Context, req *config.Request) (*drone.Config, error) {
	entry, _, _, err := p.client.Repositories.GetContents(ctx, req.Repo.Namespace, req.Repo.Name, req.Repo.Config, &github.RepositoryContentGetOptions{Ref: req.Build.After})
	if err != nil {
		return nil, err
	}
	entryBody, err := entry.GetContent()
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(strings.NewReader(entryBody))
	var records []map[string]interface{}
	var dependsOn []string
	var statuses []*github.RepoStatus
	for {
		record := map[string]interface{}{}
		if err := decoder.Decode(&record); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		if record["kind"] == "virtual-pipeline" {
			recordBytes, _ := json.Marshal(&record)
			recordStr := string(recordBytes)
			pipelines := gjson.Get(recordStr, "pipelines")

			for _, item := range pipelines.Array() {
				key := item.String()

				droneYAML := filepath.Join(key, req.Repo.Config)
				// TODO parallelism of fetching drone.yml
				content, _, _, err := p.client.Repositories.GetContents(ctx, req.Repo.Namespace, req.Repo.Name, droneYAML, &github.RepositoryContentGetOptions{Ref: req.Build.After})
				if err != nil {
					return nil, err
				}

				body, err := content.GetContent()
				if err != nil {
					return nil, err
				}
				record := map[string]interface{}{}
				if err := yaml.Unmarshal([]byte(body), &record); err != nil {
					return nil, err
				}
				recordBytes, _ := json.Marshal(&record)
				recordStr := string(recordBytes)
				dependsOn = append(dependsOn, gjson.Get(recordStr, "name").String())
				statuses = append(statuses, &github.RepoStatus{
					TargetURL: github.String(req.Repo.HTTPURL + "/blob/" + req.Build.After + "/" + droneYAML),
					State:     github.String("success"),
					Context:   github.String("config-merge/" + droneYAML),
				})
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

	go func(owner, repo, ref string, statues []*github.RepoStatus) {
		for _, stauts := range statuses {
			_, _, err := p.client.Repositories.CreateStatus(context.Background(), owner, repo, ref, stauts)
			if err != nil {
				logrus.Errorf("unable to publish statuses: " + err.Error())
			}
		}
	}(req.Repo.Namespace, req.Repo.Name, req.Build.After, statuses)

	return &drone.Config{
		Data: output.String(),
	}, nil
}
