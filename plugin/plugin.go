package plugin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/drone/drone-go/drone"
	"github.com/drone/drone-go/plugin/config"
	"github.com/google/go-github/github"
	"github.com/sirupsen/logrus"
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
	var statuses []*github.RepoStatus
	templates := map[string]*template.Template{}

	var handleRecord func(record map[string]interface{}) error
	handleRecord = func(record map[string]interface{}) error {
		switch record["kind"] {
		case "template-pipeline":
			{
				str := record["template"].(string)
				name := record["name"].(string)
				tpl, err := template.New(str).Parse(str)
				if err != nil {
					return err
				}
				templates[name] = tpl
				return nil
			}
		case "from-pipeline-template":
			{
				name := record["template"].(string)
				tpl, ok := templates[name]
				if !ok {
					return fmt.Errorf("unable to find template: " + name)
				}
				buf := bytes.NewBuffer(nil)
				if err := tpl.Execute(buf, record["variables"]); err != nil {
					return err
				}
				templateRecord := map[string]interface{}{}
				if err := yaml.Unmarshal(buf.Bytes(), &templateRecord); err != nil {
					return err
				}
				records = append(records, templateRecord)
				return nil
			}
		case "virtual-pipeline":
			{
				pipelines := record["pipelines"].([]interface{})
				for _, item := range pipelines {
					key := item.(string)
					droneYAML := filepath.Join(key, req.Repo.Config)
					// TODO parallelism of fetching drone.yml
					content, _, _, err := p.client.Repositories.GetContents(ctx, req.Repo.Namespace, req.Repo.Name, droneYAML, &github.RepositoryContentGetOptions{Ref: req.Build.After})
					if err != nil {
						return err
					}

					body, err := content.GetContent()
					if err != nil {
						return err
					}
					childRecord := map[string]interface{}{}
					if err := yaml.Unmarshal([]byte(body), &childRecord); err != nil {
						return err
					}
					statuses = append(statuses, &github.RepoStatus{
						TargetURL: github.String(req.Repo.HTTPURL + "/blob/" + req.Build.After + "/" + droneYAML),
						State:     github.String("success"),
						Context:   github.String("config-merge/" + droneYAML),
					})
					if err := handleRecord(childRecord); err != nil {
						return err
					}
				}
				return nil
			}
		default:
			records = append(records, record)
		}
		return nil
	}

	for {
		record := map[string]interface{}{}
		if err := decoder.Decode(&record); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if err := handleRecord(record); err != nil {
			return nil, err
		}
	}

	output := bytes.NewBuffer(nil)
	encoder := yaml.NewEncoder(output)
	for _, record := range records {
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
